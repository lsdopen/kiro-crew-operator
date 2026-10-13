package authz

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

// maxReasonLen caps the free-text reason, in characters.
const maxReasonLen = 500

var (
	// ErrForbidden means the account may not perform the action. Nothing was written.
	ErrForbidden = errors.New("forbidden")
	// ErrNotFound means the request does not exist.
	ErrNotFound = errors.New("request not found")
	// ErrNotPending means the request already has a decision or a final phase.
	ErrNotPending = errors.New("request is not pending")
	// ErrConflict means the request changed while it was being written.
	ErrConflict = errors.New("request changed, reload and retry")
	// ErrNoNamespace means writes need console.crewNamespace.
	ErrNoNamespace = errors.New("console.crewNamespace is not set")
	// ErrOpenRequest means the requester already has a request in flight.
	ErrOpenRequest = errors.New("you already have an open request")
	// ErrHasCrew means the requester already owns a crew.
	ErrHasCrew = errors.New("you already have a crew")
)

// InvalidError is a problem with form input, safe to show to the user.
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

// NewRequest is the form input of a request. Identity is deliberately absent:
// the requester is always the session account.
type NewRequest struct {
	Size           string
	Up, Down, Zone string
	Reason         string
}

// Validate checks the input and returns the size and schedule to store.
// Cron expressions use the same parser as the crew controller.
func (n NewRequest) Validate() (v1alpha1.Size, *v1alpha1.ScheduleSpec, error) {
	size := v1alpha1.Size(n.Size)
	if _, ok := v1alpha1.SizeProfiles[size]; !ok {
		return "", nil, &InvalidError{"unknown size"}
	}
	if utf8.RuneCountInString(n.Reason) > maxReasonLen {
		return "", nil, &InvalidError{fmt.Sprintf("reason is longer than %d characters", maxReasonLen)}
	}
	up, down, zone := strings.TrimSpace(n.Up), strings.TrimSpace(n.Down), strings.TrimSpace(n.Zone)
	if up == "" && down == "" {
		if zone != "" {
			return "", nil, &InvalidError{"a timezone needs an up and down schedule"}
		}
		return size, nil, nil
	}
	if up == "" || down == "" {
		return "", nil, &InvalidError{"set both up and down, or neither"}
	}
	if zone == "" {
		zone = "UTC"
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return "", nil, &InvalidError{fmt.Sprintf("invalid timezone %q", zone)}
	}
	for _, f := range []struct{ label, expr string }{{"up", up}, {"down", down}} {
		sched, err := cron.ParseStandard(f.expr)
		if err != nil {
			return "", nil, &InvalidError{fmt.Sprintf("invalid %s schedule %q", f.label, f.expr)}
		}
		if sched.Next(time.Now()).IsZero() {
			return "", nil, &InvalidError{fmt.Sprintf("%s schedule %q never fires", f.label, f.expr)}
		}
	}
	// The CRD requires at least 9 characters; fail here with a readable message.
	if len(up) < 9 || len(down) < 9 {
		return "", nil, &InvalidError{"schedules must be 5-field cron expressions, e.g. 0 8 * * 1-5"}
	}
	return size, &v1alpha1.ScheduleSpec{Up: up, Down: down, Timezone: zone}, nil
}

// CanDecide is true for an admin, or for a manager whose report is the requester.
// requester may be nil when the requester has no account, which leaves admins.
func CanDecide(me, requester *v1alpha1.KiroCrewAccount) bool {
	switch me.Spec.Role {
	case v1alpha1.RoleAdmin:
		return true
	case v1alpha1.RoleManager:
		return requester != nil && requester.Spec.Manager != "" &&
			strings.EqualFold(requester.Spec.Manager, me.Spec.Email)
	default:
		return false
	}
}

// Pending is true until a decision is written or the controller moves the phase.
func Pending(q *v1alpha1.KiroCrewRequest) bool {
	phase := q.Status.Phase
	return q.Spec.Decision == nil && (phase == "" || phase == v1alpha1.PhasePending)
}

func isOpen(q *v1alpha1.KiroCrewRequest) bool {
	switch q.Status.Phase {
	case v1alpha1.PhaseDenied, v1alpha1.PhaseProvisioned, v1alpha1.PhaseInvalid:
		return false
	}
	return true
}

// CreateRequest stores a request for me. spec.requester is me, never input.
func (s *Store) CreateRequest(ctx context.Context, me *v1alpha1.KiroCrewAccount, in NewRequest) (*v1alpha1.KiroCrewRequest, error) {
	if s.CrewNamespace == "" {
		return nil, ErrNoNamespace
	}
	size, schedule, err := in.Validate()
	if err != nil {
		return nil, err
	}
	ns := client.InNamespace(s.CrewNamespace)
	var reqs v1alpha1.KiroCrewRequestList
	if err := s.Client.List(ctx, &reqs, ns); err != nil {
		return nil, fmt.Errorf("listing requests: %w", err)
	}
	for i := range reqs.Items {
		if strings.EqualFold(reqs.Items[i].Spec.Requester, me.Spec.Email) && isOpen(&reqs.Items[i]) {
			return nil, ErrOpenRequest
		}
	}
	var crews v1alpha1.KiroCrewList
	if err := s.Client.List(ctx, &crews, ns); err != nil {
		return nil, fmt.Errorf("listing crews: %w", err)
	}
	for i := range crews.Items {
		if strings.EqualFold(crews.Items[i].Spec.Owner, me.Spec.Email) {
			return nil, ErrHasCrew
		}
	}

	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return nil, fmt.Errorf("naming request: %w", err)
	}
	q := &v1alpha1.KiroCrewRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "req-" + AccountName(me.Spec.Email) + "-" + hex.EncodeToString(suffix),
			Namespace: s.CrewNamespace,
		},
		Spec: v1alpha1.KiroCrewRequestSpec{
			Requester: me.Spec.Email, Size: size, Schedule: schedule, Reason: strings.TrimSpace(in.Reason),
		},
	}
	if err := s.Client.Create(ctx, q); err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	return q, nil
}

// Decide records verdict on a Pending request. decision.by is me, never input.
// Authorisation is checked before the request's state, and a refusal writes nothing.
func (s *Store) Decide(ctx context.Context, me *v1alpha1.KiroCrewAccount, name string, verdict v1alpha1.Verdict) error {
	if verdict != v1alpha1.VerdictApproved && verdict != v1alpha1.VerdictDenied {
		return &InvalidError{"verdict must be approved or denied"}
	}
	q, err := s.liveRequest(ctx, name)
	if err != nil {
		return err
	}
	var accounts v1alpha1.KiroCrewAccountList
	if err := s.Client.List(ctx, &accounts); err != nil {
		return fmt.Errorf("listing accounts: %w", err)
	}
	if !CanDecide(me, FindAccount(accounts.Items, q.Spec.Requester)) {
		return ErrForbidden
	}
	if !Pending(q) {
		return ErrNotPending
	}
	orig := q.DeepCopy()
	q.Spec.Decision = &v1alpha1.Decision{Verdict: verdict, By: me.Spec.Email}
	// The resourceVersion in the patch makes a concurrent decision a conflict.
	return conflictOr(s.Client.Patch(ctx, q, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})))
}

// Cancel deletes me's own Pending request.
func (s *Store) Cancel(ctx context.Context, me *v1alpha1.KiroCrewAccount, name string) error {
	q, err := s.liveRequest(ctx, name)
	if err != nil {
		return err
	}
	if !strings.EqualFold(q.Spec.Requester, me.Spec.Email) {
		return ErrForbidden
	}
	if !Pending(q) {
		return ErrNotPending
	}
	return conflictOr(s.Client.Delete(ctx, q, client.Preconditions{UID: &q.UID, ResourceVersion: &q.ResourceVersion}))
}

func (s *Store) liveRequest(ctx context.Context, name string) (*v1alpha1.KiroCrewRequest, error) {
	if s.CrewNamespace == "" {
		return nil, ErrNoNamespace
	}
	q := &v1alpha1.KiroCrewRequest{}
	err := s.Live.Get(ctx, client.ObjectKey{Namespace: s.CrewNamespace, Name: name}, q)
	if apierrors.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading request: %w", err)
	}
	return q, nil
}

func conflictOr(err error) error {
	if apierrors.IsConflict(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("writing request: %w", err)
	}
	return nil
}
