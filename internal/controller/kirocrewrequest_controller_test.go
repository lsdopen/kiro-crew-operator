package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kirocrewv1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

const (
	reqNamespace = "default"
	reqRequester = "alice@example.com"
	reqCrewName  = "alice"
	reqManager   = "boss@example.com"
	reqAdmin     = "root@example.com"
	reqStranger  = "mallory@example.com"
)

var _ = Describe("KiroCrewRequest Controller", func() {
	ctx := context.Background()
	var reconciler *KiroCrewRequestReconciler

	newAccount := func(name, email string, role kirocrewv1alpha1.Role, manager string) {
		GinkgoHelper()
		Expect(k8sClient.Create(ctx, &kirocrewv1alpha1.KiroCrewAccount{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       kirocrewv1alpha1.KiroCrewAccountSpec{Email: email, Role: role, Manager: manager},
		})).To(Succeed())
	}

	// newRequest creates a request for requester, decided by `by` when non-empty.
	newRequest := func(requester string, verdict kirocrewv1alpha1.Verdict, by string) types.NamespacedName {
		GinkgoHelper()
		request := &kirocrewv1alpha1.KiroCrewRequest{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "req-", Namespace: reqNamespace},
			Spec: kirocrewv1alpha1.KiroCrewRequestSpec{
				Requester: requester,
				Size:      kirocrewv1alpha1.SizePower,
				Schedule:  &kirocrewv1alpha1.ScheduleSpec{Up: "0 8 * * 1-5", Down: "0 18 * * 1-5", Timezone: "UTC"},
			},
		}
		if by != "" {
			request.Spec.Decision = &kirocrewv1alpha1.Decision{Verdict: verdict, By: by}
		}
		Expect(k8sClient.Create(ctx, request)).To(Succeed())
		return client.ObjectKeyFromObject(request)
	}

	reconcileRequest := func(key types.NamespacedName) *kirocrewv1alpha1.KiroCrewRequest {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		request := &kirocrewv1alpha1.KiroCrewRequest{}
		Expect(k8sClient.Get(ctx, key, request)).To(Succeed())
		return request
	}

	crewExists := func(name string) bool {
		GinkgoHelper()
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: reqNamespace, Name: name}, &kirocrewv1alpha1.KiroCrew{})
		if apierrors.IsNotFound(err) {
			return false
		}
		Expect(err).NotTo(HaveOccurred())
		return true
	}

	BeforeEach(func() {
		reconciler = &KiroCrewRequestReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		newAccount("req-alice", reqRequester, kirocrewv1alpha1.RoleUser, reqManager)
		newAccount("req-boss", reqManager, kirocrewv1alpha1.RoleManager, "")
		newAccount("req-root", reqAdmin, kirocrewv1alpha1.RoleAdmin, "")
		newAccount("req-mallory", reqStranger, kirocrewv1alpha1.RoleUser, "")
	})

	AfterEach(func() {
		Expect(k8sClient.DeleteAllOf(ctx, &kirocrewv1alpha1.KiroCrewAccount{})).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &kirocrewv1alpha1.KiroCrewRequest{}, client.InNamespace(reqNamespace))).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &kirocrewv1alpha1.KiroCrew{}, client.InNamespace(reqNamespace))).To(Succeed())
	})

	It("provisions a crew when the manager approves", func() {
		key := newRequest(reqRequester, kirocrewv1alpha1.VerdictApproved, reqManager)
		request := reconcileRequest(key)

		Expect(request.Status.Phase).To(Equal(kirocrewv1alpha1.PhaseProvisioned))
		Expect(request.Status.CrewRef).To(Equal(reqCrewName))
		Expect(request.Status.DecidedBy).To(Equal(reqManager))
		Expect(request.Status.DecidedAt).NotTo(BeNil())

		crew := &kirocrewv1alpha1.KiroCrew{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: reqNamespace, Name: reqCrewName}, crew)).To(Succeed())
		Expect(crew.Spec.Owner).To(Equal(reqRequester))
		Expect(crew.Spec.Size).To(Equal(kirocrewv1alpha1.SizePower))
		Expect(crew.Spec.Schedule).NotTo(BeNil())
		Expect(crew.Spec.Schedule.Up).To(Equal("0 8 * * 1-5"))
		Expect(metav1.IsControlledBy(crew, request)).To(BeTrue())
	})

	It("provisions a crew when an admin approves", func() {
		request := reconcileRequest(newRequest(reqRequester, kirocrewv1alpha1.VerdictApproved, reqAdmin))

		Expect(request.Status.Phase).To(Equal(kirocrewv1alpha1.PhaseProvisioned))
		Expect(crewExists(reqCrewName)).To(BeTrue())
	})

	It("rejects an approval from an unrelated user", func() {
		request := reconcileRequest(newRequest(reqRequester, kirocrewv1alpha1.VerdictApproved, reqStranger))

		Expect(request.Status.Phase).To(Equal(kirocrewv1alpha1.PhaseInvalid))
		Expect(request.Status.Conditions).To(ContainElement(
			HaveField("Reason", kirocrewv1alpha1.ReasonUnauthorisedApprover)))
		Expect(crewExists(reqCrewName)).To(BeFalse())
	})

	It("records a denial and creates no crew", func() {
		request := reconcileRequest(newRequest(reqRequester, kirocrewv1alpha1.VerdictDenied, reqManager))

		Expect(request.Status.Phase).To(Equal(kirocrewv1alpha1.PhaseDenied))
		Expect(request.Status.DecidedBy).To(Equal(reqManager))
		Expect(crewExists(reqCrewName)).To(BeFalse())
	})

	It("marks a request without an account Invalid", func() {
		request := reconcileRequest(newRequest("ghost@example.com", kirocrewv1alpha1.VerdictApproved, reqAdmin))

		Expect(request.Status.Phase).To(Equal(kirocrewv1alpha1.PhaseInvalid))
		Expect(request.Status.Conditions).To(ContainElement(
			HaveField("Reason", kirocrewv1alpha1.ReasonUnknownRequester)))
		Expect(crewExists("ghost")).To(BeFalse())
	})

	It("stays Pending until a decision is made", func() {
		request := reconcileRequest(newRequest(reqRequester, "", ""))

		Expect(request.Status.Phase).To(Equal(kirocrewv1alpha1.PhasePending))
		Expect(crewExists(reqCrewName)).To(BeFalse())
	})

	It("provisions a crew once the console patches a decision onto a Pending request", func() {
		key := newRequest(reqRequester, "", "")
		Expect(reconcileRequest(key).Status.Phase).To(Equal(kirocrewv1alpha1.PhasePending))

		// The console's write: a merge patch of spec.decision with an optimistic lock.
		request := &kirocrewv1alpha1.KiroCrewRequest{}
		Expect(k8sClient.Get(ctx, key, request)).To(Succeed())
		orig := request.DeepCopy()
		request.Spec.Decision = &kirocrewv1alpha1.Decision{Verdict: kirocrewv1alpha1.VerdictApproved, By: reqManager}
		Expect(k8sClient.Patch(ctx, request, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}))).To(Succeed())

		request = reconcileRequest(key)
		Expect(request.Status.Phase).To(Equal(kirocrewv1alpha1.PhaseProvisioned))
		Expect(request.Status.DecidedBy).To(Equal(reqManager))
		Expect(crewExists(reqCrewName)).To(BeTrue())
	})

	It("creates one crew however often it reconciles", func() {
		key := newRequest(reqRequester, kirocrewv1alpha1.VerdictApproved, reqManager)
		first := reconcileRequest(key)
		second := reconcileRequest(key)

		Expect(second.Status.Phase).To(Equal(kirocrewv1alpha1.PhaseProvisioned))
		Expect(second.Status.DecidedAt).To(Equal(first.Status.DecidedAt))
		crews := &kirocrewv1alpha1.KiroCrewList{}
		Expect(k8sClient.List(ctx, crews, client.InNamespace(reqNamespace))).To(Succeed())
		Expect(crews.Items).To(HaveLen(1))
	})

	It("adopts the crew a previous reconcile created before its status update", func() {
		key := newRequest(reqRequester, kirocrewv1alpha1.VerdictApproved, reqManager)
		reconcileRequest(key)
		// Simulate the status write being lost after the crew was created.
		request := &kirocrewv1alpha1.KiroCrewRequest{}
		Expect(k8sClient.Get(ctx, key, request)).To(Succeed())
		request.Status = kirocrewv1alpha1.KiroCrewRequestStatus{}
		Expect(k8sClient.Status().Update(ctx, request)).To(Succeed())

		request = reconcileRequest(key)

		Expect(request.Status.Phase).To(Equal(kirocrewv1alpha1.PhaseProvisioned))
		crews := &kirocrewv1alpha1.KiroCrewList{}
		Expect(k8sClient.List(ctx, crews, client.InNamespace(reqNamespace))).To(Succeed())
		Expect(crews.Items).To(HaveLen(1))
	})

	It("marks the request Invalid when the crew name belongs to another owner", func() {
		Expect(k8sClient.Create(ctx, &kirocrewv1alpha1.KiroCrew{
			ObjectMeta: metav1.ObjectMeta{Name: reqCrewName, Namespace: reqNamespace},
			Spec:       kirocrewv1alpha1.KiroCrewSpec{Owner: "other@example.com"},
		})).To(Succeed())

		request := reconcileRequest(newRequest(reqRequester, kirocrewv1alpha1.VerdictApproved, reqManager))

		Expect(request.Status.Phase).To(Equal(kirocrewv1alpha1.PhaseInvalid))
		Expect(request.Status.Conditions).To(ContainElement(
			HaveField("Reason", kirocrewv1alpha1.ReasonCrewNameTaken)))
		crew := &kirocrewv1alpha1.KiroCrew{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: reqNamespace, Name: reqCrewName}, crew)).To(Succeed())
		Expect(crew.Spec.Owner).To(Equal("other@example.com"))
	})
})

var _ = DescribeTable("crewName",
	func(email, want string) { Expect(crewName(email)).To(Equal(want)) },
	Entry("plain", "alice@example.com", "alice"),
	Entry("dots and plus", "Alice.Smith+x@example.com", "alice-smith-x"),
	Entry("underscores trimmed", "_bob_@example.com", "bob"),
	Entry("long local part", "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz@example.com", "abcdefghijklmnopqrstuvwxyzabcdefghijklmn"),
	Entry("nothing usable", "...@example.com", "crew"),
)
