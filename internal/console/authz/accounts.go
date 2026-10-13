package authz

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

var (
	// ErrAccountNotFound means the account does not exist.
	ErrAccountNotFound = errors.New("account not found")
	// ErrLastAdmin means the write would leave the platform with no admin.
	ErrLastAdmin = errors.New("cannot demote or delete the last admin")
)

var emailShape = regexp.MustCompile(`^[^@\s]+@[^@\s]+$`)

// AccountEdit is the form input of an account update. Email is deliberately absent.
type AccountEdit struct {
	Role, Manager string
}

func (e AccountEdit) validate(self string) (v1alpha1.Role, string, error) {
	role := v1alpha1.Role(e.Role)
	switch role {
	case v1alpha1.RoleAdmin, v1alpha1.RoleManager, v1alpha1.RoleUser:
	default:
		return "", "", &InvalidError{"unknown role"}
	}
	manager := strings.TrimSpace(e.Manager)
	if manager == "" {
		return role, "", nil
	}
	if !emailShape.MatchString(manager) || !isASCII(manager) {
		return "", "", &InvalidError{"manager must be an ASCII email address"}
	}
	if EmailEqual(manager, self) {
		return "", "", &InvalidError{"an account cannot be its own manager"}
	}
	return role, manager, nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// UpdateAccount sets the role and manager of the named account. Admin only.
func (s *Store) UpdateAccount(ctx context.Context, me *v1alpha1.KiroCrewAccount, name string, in AccountEdit) error {
	if me.Spec.Role != v1alpha1.RoleAdmin {
		return ErrForbidden
	}
	// Serialises the last-admin check within this replica only; replicas do not coordinate.
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	a, err := s.liveAccount(ctx, name)
	if err != nil {
		return err
	}
	role, manager, err := in.validate(a.Spec.Email)
	if err != nil {
		return err
	}
	if a.Spec.Role == v1alpha1.RoleAdmin && role != v1alpha1.RoleAdmin {
		if err := s.requireOtherAdmin(ctx, name); err != nil {
			return err
		}
	}
	orig := a.DeepCopy()
	a.Spec.Role, a.Spec.Manager = role, manager
	return writeErr("account", s.Client.Patch(ctx, a, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})))
}

// DeleteAccount removes the named account. Admin only.
func (s *Store) DeleteAccount(ctx context.Context, me *v1alpha1.KiroCrewAccount, name string) error {
	if me.Spec.Role != v1alpha1.RoleAdmin {
		return ErrForbidden
	}
	if name == me.Name {
		return &InvalidError{"cannot delete your own account"}
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	a, err := s.liveAccount(ctx, name)
	if err != nil {
		return err
	}
	if a.Spec.Role == v1alpha1.RoleAdmin {
		if err := s.requireOtherAdmin(ctx, name); err != nil {
			return err
		}
	}
	return writeErr("account", s.Client.Delete(ctx, a, client.Preconditions{UID: &a.UID, ResourceVersion: &a.ResourceVersion}))
}

func (s *Store) liveAccount(ctx context.Context, name string) (*v1alpha1.KiroCrewAccount, error) {
	a := &v1alpha1.KiroCrewAccount{}
	err := s.Live.Get(ctx, client.ObjectKey{Name: name}, a)
	if apierrors.IsNotFound(err) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading account: %w", err)
	}
	return a, nil
}

// requireOtherAdmin fails with ErrLastAdmin unless an admin other than name exists.
func (s *Store) requireOtherAdmin(ctx context.Context, name string) error {
	var all v1alpha1.KiroCrewAccountList
	if err := s.Live.List(ctx, &all); err != nil {
		return fmt.Errorf("listing accounts: %w", err)
	}
	for i := range all.Items {
		if all.Items[i].Name != name && all.Items[i].Spec.Role == v1alpha1.RoleAdmin {
			return nil
		}
	}
	return ErrLastAdmin
}

// writeErr maps a write error to ErrConflict or a wrapped error.
func writeErr(what string, err error) error {
	if apierrors.IsConflict(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("writing %s: %w", what, err)
	}
	return nil
}
