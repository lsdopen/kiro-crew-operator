package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kirocrewv1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

var _ = Describe("Resizing a running crew", func() {
	ctx := context.Background()
	const ns = "default"

	var requests *KiroCrewRequestReconciler

	newCrew := func(name, owner string, size kirocrewv1alpha1.Size) {
		GinkgoHelper()
		Expect(k8sClient.Create(ctx, &kirocrewv1alpha1.KiroCrew{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       kirocrewv1alpha1.KiroCrewSpec{Owner: owner, Size: size},
		})).To(Succeed())
	}

	resizeRequest := func(requester, crewRef string, size kirocrewv1alpha1.Size, verdict kirocrewv1alpha1.Verdict, by string) client.ObjectKey {
		GinkgoHelper()
		q := &kirocrewv1alpha1.KiroCrewRequest{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "resize-", Namespace: ns},
			Spec:       kirocrewv1alpha1.KiroCrewRequestSpec{Requester: requester, CrewRef: crewRef, Size: size},
		}
		if by != "" {
			q.Spec.Decision = &kirocrewv1alpha1.Decision{Verdict: verdict, By: by}
		}
		Expect(k8sClient.Create(ctx, q)).To(Succeed())
		return client.ObjectKeyFromObject(q)
	}

	reconcileRequest := func(key client.ObjectKey) *kirocrewv1alpha1.KiroCrewRequest {
		GinkgoHelper()
		_, err := requests.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		q := &kirocrewv1alpha1.KiroCrewRequest{}
		Expect(k8sClient.Get(ctx, key, q)).To(Succeed())
		return q
	}

	crewSize := func(name string) kirocrewv1alpha1.Size {
		GinkgoHelper()
		c := &kirocrewv1alpha1.KiroCrew{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, c)).To(Succeed())
		return c.Spec.Size
	}

	BeforeEach(func() {
		requests = &KiroCrewRequestReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		for name, a := range map[string]kirocrewv1alpha1.KiroCrewAccountSpec{
			"rz-alice": {Email: reqRequester, Role: kirocrewv1alpha1.RoleUser, Manager: reqManager},
			"rz-boss":  {Email: reqManager, Role: kirocrewv1alpha1.RoleManager},
			"rz-root":  {Email: reqAdmin, Role: kirocrewv1alpha1.RoleAdmin},
		} {
			Expect(k8sClient.Create(ctx, &kirocrewv1alpha1.KiroCrewAccount{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: a})).To(Succeed())
		}
	})

	AfterEach(func() {
		Expect(k8sClient.DeleteAllOf(ctx, &kirocrewv1alpha1.KiroCrewAccount{})).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &kirocrewv1alpha1.KiroCrewRequest{}, client.InNamespace(ns))).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &kirocrewv1alpha1.KiroCrew{}, client.InNamespace(ns))).To(Succeed())
	})

	Describe("the request controller", func() {
		It("resizes the named crew once the manager approves", func() {
			newCrew(reqCrewName, reqRequester, kirocrewv1alpha1.SizeLight)
			q := reconcileRequest(resizeRequest(reqRequester, reqCrewName, kirocrewv1alpha1.SizePower, kirocrewv1alpha1.VerdictApproved, reqManager))

			Expect(q.Status.Phase).To(Equal(kirocrewv1alpha1.PhaseApplied))
			Expect(q.Status.CrewRef).To(Equal(reqCrewName))
			Expect(q.Status.DecidedBy).To(Equal(reqManager))
			Expect(crewSize(reqCrewName)).To(Equal(kirocrewv1alpha1.SizePower))
		})

		It("waits for a decision and leaves the size alone", func() {
			newCrew(reqCrewName, reqRequester, kirocrewv1alpha1.SizeLight)
			q := reconcileRequest(resizeRequest(reqRequester, reqCrewName, kirocrewv1alpha1.SizePower, "", ""))

			Expect(q.Status.Phase).To(Equal(kirocrewv1alpha1.PhasePending))
			Expect(crewSize(reqCrewName)).To(Equal(kirocrewv1alpha1.SizeLight))
		})

		It("leaves the size alone when the resize is denied", func() {
			newCrew(reqCrewName, reqRequester, kirocrewv1alpha1.SizeLight)
			q := reconcileRequest(resizeRequest(reqRequester, reqCrewName, kirocrewv1alpha1.SizePower, kirocrewv1alpha1.VerdictDenied, reqManager))

			Expect(q.Status.Phase).To(Equal(kirocrewv1alpha1.PhaseDenied))
			Expect(crewSize(reqCrewName)).To(Equal(kirocrewv1alpha1.SizeLight))
		})

		It("refuses an approval from someone who is neither admin nor the owner's manager", func() {
			newCrew(reqCrewName, reqRequester, kirocrewv1alpha1.SizeLight)
			q := reconcileRequest(resizeRequest(reqRequester, reqCrewName, kirocrewv1alpha1.SizePower, kirocrewv1alpha1.VerdictApproved, reqStranger))

			Expect(q.Status.Phase).To(Equal(kirocrewv1alpha1.PhaseInvalid))
			Expect(crewSize(reqCrewName)).To(Equal(kirocrewv1alpha1.SizeLight))
		})

		It("marks a resize of a missing crew Invalid", func() {
			q := reconcileRequest(resizeRequest(reqRequester, "ghost", kirocrewv1alpha1.SizePower, kirocrewv1alpha1.VerdictApproved, reqAdmin))

			Expect(q.Status.Phase).To(Equal(kirocrewv1alpha1.PhaseInvalid))
			Expect(q.Status.Conditions).To(ContainElement(HaveField("Reason", kirocrewv1alpha1.ReasonCrewNotFound)))
		})

		It("marks a resize of someone else's crew Invalid and changes nothing", func() {
			newCrew("theirs", "other@example.com", kirocrewv1alpha1.SizeLight)
			q := reconcileRequest(resizeRequest(reqRequester, "theirs", kirocrewv1alpha1.SizePower, kirocrewv1alpha1.VerdictApproved, reqAdmin))

			Expect(q.Status.Phase).To(Equal(kirocrewv1alpha1.PhaseInvalid))
			Expect(q.Status.Conditions).To(ContainElement(HaveField("Reason", kirocrewv1alpha1.ReasonNotOwner)))
			Expect(crewSize("theirs")).To(Equal(kirocrewv1alpha1.SizeLight))
		})

		It("is final once Applied", func() {
			newCrew(reqCrewName, reqRequester, kirocrewv1alpha1.SizeLight)
			key := resizeRequest(reqRequester, reqCrewName, kirocrewv1alpha1.SizePower, kirocrewv1alpha1.VerdictApproved, reqManager)
			reconcileRequest(key)

			c := &kirocrewv1alpha1.KiroCrew{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: reqCrewName}, c)).To(Succeed())
			c.Spec.Size = kirocrewv1alpha1.SizeMini
			Expect(k8sClient.Update(ctx, c)).To(Succeed())

			Expect(reconcileRequest(key).Status.Phase).To(Equal(kirocrewv1alpha1.PhaseApplied))
			Expect(crewSize(reqCrewName)).To(Equal(kirocrewv1alpha1.SizeMini))
		})
	})

	Describe("the request schema", func() {
		update := func(key client.ObjectKey, mutate func(*kirocrewv1alpha1.KiroCrewRequestSpec)) error {
			GinkgoHelper()
			q := &kirocrewv1alpha1.KiroCrewRequest{}
			Expect(k8sClient.Get(ctx, key, q)).To(Succeed())
			mutate(&q.Spec)
			return k8sClient.Update(ctx, q)
		}

		It("rejects a resize that carries a schedule", func() {
			q := &kirocrewv1alpha1.KiroCrewRequest{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "resize-", Namespace: ns},
				Spec: kirocrewv1alpha1.KiroCrewRequestSpec{
					Requester: reqRequester, CrewRef: reqCrewName,
					Schedule: &kirocrewv1alpha1.ScheduleSpec{Up: weekdayUp, Down: weekdayDown},
				},
			}
			Expect(k8sClient.Create(ctx, q)).To(MatchError(ContainSubstring("cannot carry a schedule")))
		})

		It("keeps requester, size, schedule and crewRef fixed after create, but lets the decision change", func() {
			key := resizeRequest(reqRequester, reqCrewName, kirocrewv1alpha1.SizeLight, "", "")
			newSchedule := &kirocrewv1alpha1.ScheduleSpec{Up: weekdayUp, Down: weekdayDown}

			Expect(update(key, func(s *kirocrewv1alpha1.KiroCrewRequestSpec) { s.Size = kirocrewv1alpha1.SizePower })).
				To(MatchError(ContainSubstring("immutable")))
			Expect(update(key, func(s *kirocrewv1alpha1.KiroCrewRequestSpec) { s.Requester = reqStranger })).
				To(MatchError(ContainSubstring("immutable")))
			Expect(update(key, func(s *kirocrewv1alpha1.KiroCrewRequestSpec) { s.CrewRef = "other" })).
				To(MatchError(ContainSubstring("immutable")))
			Expect(update(key, func(s *kirocrewv1alpha1.KiroCrewRequestSpec) { s.CrewRef = "" })).
				To(MatchError(ContainSubstring("immutable")))

			Expect(update(key, func(s *kirocrewv1alpha1.KiroCrewRequestSpec) {
				s.Decision = &kirocrewv1alpha1.Decision{Verdict: kirocrewv1alpha1.VerdictApproved, By: reqManager}
			})).To(Succeed())

			By("a new request's schedule is fixed too, whether changed or removed")
			plain := &kirocrewv1alpha1.KiroCrewRequest{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "req-", Namespace: ns},
				Spec:       kirocrewv1alpha1.KiroCrewRequestSpec{Requester: reqRequester, Size: kirocrewv1alpha1.SizeLight, Schedule: newSchedule},
			}
			Expect(k8sClient.Create(ctx, plain)).To(Succeed())
			plainKey := client.ObjectKeyFromObject(plain)
			Expect(update(plainKey, func(s *kirocrewv1alpha1.KiroCrewRequestSpec) { s.Schedule.Up = "0 9 * * 1-5" })).
				To(MatchError(ContainSubstring("immutable")))
			Expect(update(plainKey, func(s *kirocrewv1alpha1.KiroCrewRequestSpec) { s.Schedule = nil })).
				To(MatchError(ContainSubstring("immutable")))
		})
	})

	Describe("the crew controller's data volume", func() {
		const (
			crewName = "grow"
			pvcName  = "data-" + namePrefix + "-" + crewName + "-0"
		)
		var crews *KiroCrewReconciler

		reconcileCrew := func() {
			GinkgoHelper()
			_, err := crews.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: crewName}})
			Expect(err).NotTo(HaveOccurred())
		}
		setSize := func(size kirocrewv1alpha1.Size) {
			GinkgoHelper()
			c := &kirocrewv1alpha1.KiroCrew{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: crewName}, c)).To(Succeed())
			c.Spec.Size = size
			Expect(k8sClient.Update(ctx, c)).To(Succeed())
		}
		pvcSize := func() string {
			GinkgoHelper()
			p := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: pvcName}, p)).To(Succeed())
			q := p.Spec.Resources.Requests[corev1.ResourceStorage]
			return q.String()
		}
		// boundPVC creates the claim the StatefulSet controller would, bound to a class.
		boundPVC := func(class string, size string) {
			GinkgoHelper()
			p := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: ns},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: new(class),
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, p)).To(Succeed())
			p.Status.Phase = corev1.ClaimBound
			Expect(k8sClient.Status().Update(ctx, p)).To(Succeed())
		}
		storageClass := func(name string, expandable bool) {
			GinkgoHelper()
			Expect(k8sClient.Create(ctx, &storagev1.StorageClass{
				ObjectMeta:           metav1.ObjectMeta{Name: name},
				Provisioner:          "example.com/none",
				AllowVolumeExpansion: new(expandable),
			})).To(Succeed())
		}
		storageResized := func() *metav1.Condition {
			GinkgoHelper()
			c := &kirocrewv1alpha1.KiroCrew{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: crewName}, c)).To(Succeed())
			return findCondition(c.Status.Conditions, kirocrewv1alpha1.ConditionStorageResized)
		}

		BeforeEach(func() {
			crews = &KiroCrewReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			newCrew(crewName, reqRequester, kirocrewv1alpha1.SizeMini)
		})

		AfterEach(func() {
			// envtest has no controller to clear the protection finalizer.
			p := &corev1.PersistentVolumeClaim{}
			if k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: pvcName}, p) == nil {
				p.Finalizers = nil
				Expect(k8sClient.Update(ctx, p)).To(Succeed())
				Expect(k8sClient.Delete(ctx, p)).To(Succeed())
			}
			_ = k8sClient.DeleteAllOf(ctx, &storagev1.StorageClass{})
		})

		It("grows to the new tier's storage and never shrinks", func() {
			storageClass("expandable", true)
			boundPVC("expandable", kirocrewv1alpha1.SizeProfiles[kirocrewv1alpha1.SizeMini].Storage)
			reconcileCrew()
			Expect(pvcSize()).To(Equal("20Gi"))

			setSize(kirocrewv1alpha1.SizePower)
			reconcileCrew()
			Expect(pvcSize()).To(Equal("80Gi"))
			Expect(storageResized()).To(BeNil())

			setSize(kirocrewv1alpha1.SizeMini)
			reconcileCrew()
			Expect(pvcSize()).To(Equal("80Gi"))
		})

		It("keeps going and reports StorageResized=False when the class forbids expansion", func() {
			storageClass("fixed", false)
			boundPVC("fixed", kirocrewv1alpha1.SizeProfiles[kirocrewv1alpha1.SizeMini].Storage)

			setSize(kirocrewv1alpha1.SizePower)
			reconcileCrew()

			Expect(pvcSize()).To(Equal("20Gi"))
			cond := storageResized()
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(kirocrewv1alpha1.ReasonResizeRefused))
		})

		It("does nothing before the volume exists", func() {
			setSize(kirocrewv1alpha1.SizePower)
			reconcileCrew()
			Expect(storageResized()).To(BeNil())
		})
	})
})
