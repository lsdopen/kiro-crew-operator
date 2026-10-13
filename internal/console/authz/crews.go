package authz

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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

// crewPatchFields are the only KiroCrew spec fields the console writes. size is
// absent on purpose: only the request controller sets it, after an approval.
var crewPatchFields = []string{"suspend", "startedAt", "schedule", "mcpConfigRef"}

// controllable reads the crew and the accounts, and refuses unless me may control it.
func (s *Store) controllable(ctx context.Context, me *v1alpha1.KiroCrewAccount, name string) (*v1alpha1.KiroCrew, *v1alpha1.KiroCrewAccount, error) {
	crew, err := s.LiveCrew(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	var accounts v1alpha1.KiroCrewAccountList
	if err := s.Client.List(ctx, &accounts); err != nil {
		return nil, nil, fmt.Errorf("listing accounts: %w", err)
	}
	owner := FindAccount(accounts.Items, crew.Spec.Owner)
	if !CanControl(me, owner, crew) {
		return nil, nil, ErrForbidden
	}
	return crew, owner, nil
}

// patchCrew merge-patches spec, which may only hold crewPatchFields.
func (s *Store) patchCrew(ctx context.Context, crew *v1alpha1.KiroCrew, spec map[string]any) error {
	for k := range spec {
		if !slices.Contains(crewPatchFields, k) {
			return fmt.Errorf("crew field %q is not writable by the console", k)
		}
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

// SetRunning starts or stops a crew. Start clears suspend and stamps startedAt
// with now; Stop sets suspend. Only those fields are patched, and a refusal
// writes nothing.
func (s *Store) SetRunning(ctx context.Context, me *v1alpha1.KiroCrewAccount, name string, start bool, now time.Time) error {
	crew, _, err := s.controllable(ctx, me, name)
	if err != nil {
		return err
	}
	spec := map[string]any{"suspend": !start}
	if start {
		spec["startedAt"] = metav1.NewTime(now)
	}
	return s.patchCrew(ctx, crew, spec)
}

// SetSchedule replaces a crew's schedule; blank up and down remove it. The crew's
// owner, the owner's manager and admins may do this.
func (s *Store) SetSchedule(ctx context.Context, me *v1alpha1.KiroCrewAccount, name, up, down, zone string) error {
	crew, _, err := s.controllable(ctx, me, name)
	if err != nil {
		return err
	}
	schedule, err := parseSchedule(up, down, zone)
	if err != nil {
		return err
	}
	// A nil schedule marshals to null, which a merge patch reads as "remove".
	return s.patchCrew(ctx, crew, map[string]any{"schedule": schedule})
}

// SetMCPConfig sets a crew's MCP ConfigMap from the config's allowlist; blank clears it. Admin only.
func (s *Store) SetMCPConfig(ctx context.Context, me *v1alpha1.KiroCrewAccount, name, ref string) error {
	if me.Spec.Role != v1alpha1.RoleAdmin {
		return ErrForbidden
	}
	crew, err := s.LiveCrew(ctx, name)
	if err != nil {
		return err
	}
	ref = strings.TrimSpace(ref)
	if ref != "" {
		cfg, err := s.configSpec(ctx)
		if err != nil {
			return err
		}
		if !slices.Contains(cfg.MCPConfigs, ref) {
			return &InvalidError{"MCP config is not in the allowed list"}
		}
	}
	return s.patchCrew(ctx, crew, map[string]any{"mcpConfigRef": ref})
}

// ResizePinned is true when an admin pinned the crew's resources, which beat the size tier.
func ResizePinned(crew *v1alpha1.KiroCrew) bool {
	return crew.Spec.Gateway.Resources != nil || crew.Spec.Storage.Size != nil
}

// RequestResize files a resize request for a crew; the request controller sets
// the size after approval, never the console. The owner's request waits for an
// approver. A manager or admin resizing someone else's crew files it for the
// owner and approves it in the same write, with decision.by as themselves.
func (s *Store) RequestResize(ctx context.Context, me *v1alpha1.KiroCrewAccount, name, sizeIn, reason string) (*v1alpha1.KiroCrewRequest, error) {
	crew, owner, err := s.controllable(ctx, me, name)
	if err != nil {
		return nil, err
	}
	size := v1alpha1.Size(sizeIn)
	if _, ok := v1alpha1.SizeProfiles[size]; !ok {
		return nil, &InvalidError{msgUnknownSize}
	}
	if utf8.RuneCountInString(reason) > maxReasonLen {
		return nil, &InvalidError{fmt.Sprintf("reason is longer than %d characters", maxReasonLen)}
	}
	if ResizePinned(crew) {
		return nil, &InvalidError{"resources are pinned by an admin"}
	}
	if crew.Spec.Size == size {
		return nil, &InvalidError{"the crew is already that size"}
	}
	cfg, err := s.configSpec(ctx)
	if err != nil {
		return nil, err
	}
	if allowed, _ := cfg.Sizes(); !slices.Contains(allowed, size) {
		return nil, &InvalidError{"size is not allowed"}
	}
	if owner == nil {
		return nil, &InvalidError{"the crew's owner has no account"}
	}
	var reqs v1alpha1.KiroCrewRequestList
	if err := s.Client.List(ctx, &reqs, client.InNamespace(s.CrewNamespace)); err != nil {
		return nil, fmt.Errorf("listing requests: %w", err)
	}
	for i := range reqs.Items {
		if EmailEqual(reqs.Items[i].Spec.Requester, crew.Spec.Owner) && isOpen(&reqs.Items[i]) {
			return nil, ErrOpenRequest
		}
	}
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return nil, fmt.Errorf("naming request: %w", err)
	}
	q := &v1alpha1.KiroCrewRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "req-" + AccountName(crew.Spec.Owner) + "-" + hex.EncodeToString(suffix),
			Namespace: s.CrewNamespace,
		},
		Spec: v1alpha1.KiroCrewRequestSpec{
			Requester: crew.Spec.Owner, Size: size, CrewRef: crew.Name, Reason: strings.TrimSpace(reason),
		},
	}
	if !IsOwner(me, crew) && CanDecide(me, owner) {
		q.Spec.Decision = &v1alpha1.Decision{Verdict: v1alpha1.VerdictApproved, By: me.Spec.Email}
	}
	if err := s.Client.Create(ctx, q); err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	return q, nil
}
