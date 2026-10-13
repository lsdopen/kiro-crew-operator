package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

// ErrCrewNotFound means the crew does not exist.
var ErrCrewNotFound = errors.New("crew not found")

// IsOwner is true when me owns the crew.
func IsOwner(me *v1alpha1.KiroCrewAccount, crew *v1alpha1.KiroCrew) bool {
	return EmailEqual(crew.Spec.Owner, me.Spec.Email)
}

// CanControl is true for the crew's owner, the owner's manager, or an admin.
// owner may be nil when the owner has no account, which leaves the owner and admins.
func CanControl(me, owner *v1alpha1.KiroCrewAccount, crew *v1alpha1.KiroCrew) bool {
	switch {
	case IsOwner(me, crew), me.Spec.Role == v1alpha1.RoleAdmin:
		return true
	case me.Spec.Role == v1alpha1.RoleManager:
		return owner != nil && owner.Spec.Manager != "" && EmailEqual(owner.Spec.Manager, me.Spec.Email)
	default:
		return false
	}
}

// LiveCrew reads a crew straight from the API server.
func (s *Store) LiveCrew(ctx context.Context, name string) (*v1alpha1.KiroCrew, error) {
	if s.CrewNamespace == "" {
		return nil, ErrNoNamespace
	}
	crew := &v1alpha1.KiroCrew{}
	err := s.Live.Get(ctx, client.ObjectKey{Namespace: s.CrewNamespace, Name: name}, crew)
	if apierrors.IsNotFound(err) {
		return nil, ErrCrewNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading crew: %w", err)
	}
	return crew, nil
}

// OwnedCrew returns the crew only when me owns it. Managers and admins are refused.
func (s *Store) OwnedCrew(ctx context.Context, me *v1alpha1.KiroCrewAccount, name string) (*v1alpha1.KiroCrew, error) {
	crew, err := s.LiveCrew(ctx, name)
	if err != nil {
		return nil, err
	}
	if !IsOwner(me, crew) {
		return nil, ErrForbidden
	}
	return crew, nil
}

// SetRunning starts or stops a crew. Start clears suspend and stamps startedAt
// with now; Stop sets suspend. Only those fields are patched, and a refusal
// writes nothing.
func (s *Store) SetRunning(ctx context.Context, me *v1alpha1.KiroCrewAccount, name string, start bool, now time.Time) error {
	crew, err := s.LiveCrew(ctx, name)
	if err != nil {
		return err
	}
	var accounts v1alpha1.KiroCrewAccountList
	if err := s.Client.List(ctx, &accounts); err != nil {
		return fmt.Errorf("listing accounts: %w", err)
	}
	if !CanControl(me, FindAccount(accounts.Items, crew.Spec.Owner), crew) {
		return ErrForbidden
	}
	spec := map[string]any{"suspend": !start}
	if start {
		spec["startedAt"] = metav1.NewTime(now)
	}
	body, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		return fmt.Errorf("building patch: %w", err)
	}
	if err := s.Client.Patch(ctx, crew, client.RawPatch(types.MergePatchType, body)); err != nil {
		return fmt.Errorf("patching crew: %w", err)
	}
	return nil
}
