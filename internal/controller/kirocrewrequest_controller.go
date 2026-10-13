package controller

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kirocrewv1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

// maxCrewNameLen leaves room for the "kiro-crew-" prefix on generated objects
// and their suffixes within the 63-character DNS label limit.
const maxCrewNameLen = 40

var nonDNSChars = regexp.MustCompile(`[^a-z0-9]+`)

// KiroCrewRequestReconciler turns an approved KiroCrewRequest into a KiroCrew.
type KiroCrewRequestReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kirocrew.lsdopen.io,resources=kirocrewrequests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kirocrew.lsdopen.io,resources=kirocrewrequests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kirocrew.lsdopen.io,resources=kirocrewrequests/finalizers,verbs=update
// +kubebuilder:rbac:groups=kirocrew.lsdopen.io,resources=kirocrewaccounts,verbs=get;list;watch
// +kubebuilder:rbac:groups=kirocrew.lsdopen.io,resources=kirocrews,verbs=get;list;watch;create;patch

// Reconcile validates a request's decision and, if approved, creates its crew.
func (r *KiroCrewRequestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	request := &kirocrewv1alpha1.KiroCrewRequest{}
	if err := r.Get(ctx, req.NamespacedName, request); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if finished(request.Status.Phase) {
		return ctrl.Result{}, nil
	}

	before := request.Status.DeepCopy()
	if err := r.evaluate(ctx, request); err != nil {
		return ctrl.Result{}, err
	}
	if equality.Semantic.DeepEqual(before, &request.Status) {
		return ctrl.Result{}, nil
	}
	logf.FromContext(ctx).Info("Updated KiroCrewRequest", "phase", request.Status.Phase)
	return ctrl.Result{}, r.Status().Update(ctx, request)
}

// evaluate sets request.Status from the request, its accounts and its crew.
func (r *KiroCrewRequestReconciler) evaluate(ctx context.Context, request *kirocrewv1alpha1.KiroCrewRequest) error {
	accounts := &kirocrewv1alpha1.KiroCrewAccountList{}
	if err := r.List(ctx, accounts); err != nil {
		return err
	}
	requester := findAccount(accounts.Items, request.Spec.Requester)
	if requester == nil {
		setInvalid(request, kirocrewv1alpha1.ReasonUnknownRequester,
			fmt.Sprintf("No KiroCrewAccount for %s", request.Spec.Requester))
		return nil
	}

	cfg, err := loadConfigSpec(ctx, r.Client)
	if err != nil {
		return err
	}
	// A denial provisions nothing, so a later change to allowedSizes must not invalidate it.
	denied := request.Spec.Decision != nil && request.Spec.Decision.Verdict == kirocrewv1alpha1.VerdictDenied
	if allowed, _ := cfg.Sizes(); !denied && !slices.Contains(allowed, request.Spec.Size) {
		setInvalid(request, kirocrewv1alpha1.ReasonSizeNotAllowed,
			fmt.Sprintf("Size %s is not in KiroCrewConfig allowedSizes", request.Spec.Size))
		return nil
	}

	decision := request.Spec.Decision
	if decision == nil {
		setPhase(request, kirocrewv1alpha1.PhasePending, "Awaiting a decision")
		return nil
	}
	// Defence in depth only: real approver authorisation is enforced by the
	// console and RBAC, since anyone who can edit the request can write decision.by.
	if !canDecide(accounts.Items, requester, decision.By) {
		setInvalid(request, kirocrewv1alpha1.ReasonUnauthorisedApprover,
			fmt.Sprintf("%s is neither an admin nor the manager of %s", decision.By, requester.Spec.Email))
		return nil
	}

	request.Status.DecidedBy = decision.By
	if request.Status.DecidedAt == nil {
		now := metav1.Now()
		request.Status.DecidedAt = &now
	}
	if decision.Verdict == kirocrewv1alpha1.VerdictDenied {
		setPhase(request, kirocrewv1alpha1.PhaseDenied, "Request denied")
		return nil
	}
	if request.Spec.CrewRef != "" {
		return r.resize(ctx, request)
	}
	return r.provision(ctx, request)
}

// resize applies an approved request's size to the crew it names, once its owner is the requester.
func (r *KiroCrewRequestReconciler) resize(ctx context.Context, request *kirocrewv1alpha1.KiroCrewRequest) error {
	name := request.Spec.CrewRef
	crew := &kirocrewv1alpha1.KiroCrew{}
	err := r.Get(ctx, types.NamespacedName{Namespace: request.Namespace, Name: name}, crew)
	switch {
	case apierrors.IsNotFound(err):
		setInvalid(request, kirocrewv1alpha1.ReasonCrewNotFound, fmt.Sprintf("KiroCrew %s does not exist", name))
		return nil
	case err != nil:
		return err
	case !strings.EqualFold(crew.Spec.Owner, request.Spec.Requester):
		setInvalid(request, kirocrewv1alpha1.ReasonNotOwner,
			fmt.Sprintf("%s does not own KiroCrew %s", request.Spec.Requester, name))
		return nil
	}
	if crew.Spec.Size != request.Spec.Size {
		orig := crew.DeepCopy()
		crew.Spec.Size = request.Spec.Size
		if err := r.Patch(ctx, crew, client.MergeFrom(orig)); err != nil {
			return err
		}
		logf.FromContext(ctx).Info("Resized KiroCrew", "name", name, "size", request.Spec.Size)
	}
	request.Status.CrewRef = name
	setPhase(request, kirocrewv1alpha1.PhaseApplied, fmt.Sprintf("Resized KiroCrew %s to %s", name, request.Spec.Size))
	return nil
}

// finished is true for phases that need no further reconciling.
func finished(phase kirocrewv1alpha1.RequestPhase) bool {
	return phase == kirocrewv1alpha1.PhaseProvisioned || phase == kirocrewv1alpha1.PhaseApplied
}

// provision creates the crew, or adopts the one a previous reconcile created.
func (r *KiroCrewRequestReconciler) provision(ctx context.Context, request *kirocrewv1alpha1.KiroCrewRequest) error {
	name := crewName(request.Spec.Requester)
	crew := &kirocrewv1alpha1.KiroCrew{}
	err := r.Get(ctx, types.NamespacedName{Namespace: request.Namespace, Name: name}, crew)
	switch {
	case apierrors.IsNotFound(err):
		crew = &kirocrewv1alpha1.KiroCrew{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: request.Namespace},
			Spec: kirocrewv1alpha1.KiroCrewSpec{
				Owner:    request.Spec.Requester,
				Size:     request.Spec.Size,
				Schedule: request.Spec.Schedule,
			},
		}
		if err := controllerutil.SetControllerReference(request, crew, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, crew); err != nil {
			return err
		}
		logf.FromContext(ctx).Info("Created KiroCrew", "name", name)
	case err != nil:
		return err
	case !metav1.IsControlledBy(crew, request):
		setInvalid(request, kirocrewv1alpha1.ReasonCrewNameTaken,
			fmt.Sprintf("KiroCrew %s already exists and was not created by this request", name))
		return nil
	}

	request.Status.CrewRef = name
	setPhase(request, kirocrewv1alpha1.PhaseProvisioned, "Created KiroCrew "+name)
	return nil
}

func setPhase(request *kirocrewv1alpha1.KiroCrewRequest, phase kirocrewv1alpha1.RequestPhase, msg string) {
	request.Status.Phase = phase
	meta.SetStatusCondition(&request.Status.Conditions, metav1.Condition{
		Type:               kirocrewv1alpha1.ConditionRequestValid,
		Status:             metav1.ConditionTrue,
		Reason:             kirocrewv1alpha1.ReasonValid,
		Message:            msg,
		ObservedGeneration: request.Generation,
	})
}

func setInvalid(request *kirocrewv1alpha1.KiroCrewRequest, reason, msg string) {
	request.Status.Phase = kirocrewv1alpha1.PhaseInvalid
	meta.SetStatusCondition(&request.Status.Conditions, metav1.Condition{
		Type:               kirocrewv1alpha1.ConditionRequestValid,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: request.Generation,
	})
}

// canDecide is true when by is an admin account or the requester's named manager.
func canDecide(accounts []kirocrewv1alpha1.KiroCrewAccount, requester *kirocrewv1alpha1.KiroCrewAccount, by string) bool {
	if requester.Spec.Manager != "" && strings.EqualFold(requester.Spec.Manager, by) {
		return true
	}
	approver := findAccount(accounts, by)
	return approver != nil && approver.Spec.Role == kirocrewv1alpha1.RoleAdmin
}

func findAccount(accounts []kirocrewv1alpha1.KiroCrewAccount, email string) *kirocrewv1alpha1.KiroCrewAccount {
	for i := range accounts {
		if strings.EqualFold(accounts[i].Spec.Email, email) {
			return &accounts[i]
		}
	}
	return nil
}

// crewName derives a DNS-1123 name from the email's local part.
func crewName(email string) string {
	local, _, _ := strings.Cut(strings.ToLower(email), "@")
	name := strings.Trim(nonDNSChars.ReplaceAllString(local, "-"), "-")
	if len(name) > maxCrewNameLen {
		name = strings.Trim(name[:maxCrewNameLen], "-")
	}
	if name == "" {
		return "crew"
	}
	return name
}

// requestsForAccount re-queues requests from the account's email, so a request
// marked Invalid for a missing account recovers once the account exists.
func (r *KiroCrewRequestReconciler) requestsForAccount(ctx context.Context, obj client.Object) []reconcile.Request {
	account, ok := obj.(*kirocrewv1alpha1.KiroCrewAccount)
	if !ok {
		return nil
	}
	requests := &kirocrewv1alpha1.KiroCrewRequestList{}
	if err := r.List(ctx, requests); err != nil {
		return nil
	}
	var out []reconcile.Request
	for _, item := range requests.Items {
		if !finished(item.Status.Phase) &&
			(strings.EqualFold(item.Spec.Requester, account.Spec.Email) ||
				(item.Spec.Decision != nil && strings.EqualFold(item.Spec.Decision.By, account.Spec.Email))) {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&item)})
		}
	}
	return out
}

// requestsForConfig re-queues unfinished requests, so narrowing allowedSizes
// takes effect and widening it recovers requests marked Invalid for their size.
func (r *KiroCrewRequestReconciler) requestsForConfig(ctx context.Context, _ client.Object) []reconcile.Request {
	requests := &kirocrewv1alpha1.KiroCrewRequestList{}
	if err := r.List(ctx, requests); err != nil {
		return nil
	}
	var out []reconcile.Request
	for i := range requests.Items {
		if !finished(requests.Items[i].Status.Phase) {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&requests.Items[i])})
		}
	}
	return out
}

// SetupWithManager sets up the controller with the Manager.
func (r *KiroCrewRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kirocrewv1alpha1.KiroCrewRequest{}).
		Watches(&kirocrewv1alpha1.KiroCrewAccount{}, handler.EnqueueRequestsFromMapFunc(r.requestsForAccount)).
		Watches(&kirocrewv1alpha1.KiroCrewConfig{}, handler.EnqueueRequestsFromMapFunc(r.requestsForConfig)).
		Named("kirocrewrequest").
		Complete(r)
}
