package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kirocrewv1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

var _ = Describe("KiroCrew Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName      = "test-resource"
			resourceNamespace = "default"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}
		kirocrew := &kirocrewv1alpha1.KiroCrew{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind KiroCrew")
			err := k8sClient.Get(ctx, typeNamespacedName, kirocrew)
			if err != nil && errors.IsNotFound(err) {
				resource := &kirocrewv1alpha1.KiroCrew{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: resourceNamespace,
					},
					// spec.owner is required and validated: it is the instance's
					// identity boundary, so there is no meaningful empty value.
					Spec: kirocrewv1alpha1.KiroCrewSpec{
						Owner: testOwner,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &kirocrewv1alpha1.KiroCrew{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance KiroCrew")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &KiroCrewReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			name := namePrefix + "-" + resourceName

			By("creating the StatefulSet with a single container and no sidecar")
			var sts appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: name, Namespace: resourceNamespace,
			}, &sts)).To(Succeed())
			Expect(sts.Spec.Template.Spec.Containers).To(HaveLen(1))

			By("granting the gateway no more than the default size tier")
			light := kirocrewv1alpha1.SizeProfiles[kirocrewv1alpha1.SizeLight]
			gateway := sts.Spec.Template.Spec.Containers[0]
			Expect(gateway.Resources.Requests.Memory().String()).To(Equal(light.Memory))
			// Memory cannot be safely overcommitted, so request must equal limit.
			Expect(gateway.Resources.Limits.Memory().String()).To(Equal(light.Memory))
			// CPU is deliberately unlimited so idle crews are cheap.
			Expect(gateway.Resources.Limits).NotTo(HaveKey(corev1.ResourceCPU))

			By("forcing the device-code login shape")
			Expect(gateway.Env).To(ContainElement(corev1.EnvVar{
				Name: "KIRO_AUTH_INSTALL_SHAPE", Value: "remote",
			}))

			By("closing the pod network to everything")
			var np networkingv1.NetworkPolicy
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: name, Namespace: resourceNamespace,
			}, &np)).To(Succeed())
			Expect(np.Spec.Ingress).To(BeEmpty())

			By("running tailscaled in the gateway container without widening its security context")
			Expect(gateway.Env).To(ContainElements(
				corev1.EnvVar{Name: "TS_HOSTNAME", Value: name},
				corev1.EnvVar{Name: "TS_STATE_DIR", Value: "/data/tailscale"},
				corev1.EnvVar{Name: "TS_SOCKET", Value: "/var/run/tailscale/tailscaled.sock"},
			))
			for _, e := range gateway.Env {
				Expect(e.Name).NotTo(Equal("TS_SERVE_CONFIG"))
			}
			Expect(gateway.SecurityContext.Capabilities.Drop).To(ConsistOf(corev1.Capability("ALL")))
			Expect(gateway.SecurityContext.Capabilities.Add).To(BeEmpty())
			Expect(gateway.SecurityContext.AllowPrivilegeEscalation).To(HaveValue(BeFalse()))
			Expect(gateway.SecurityContext.Privileged).To(BeNil())
			Expect(gateway.SecurityContext.RunAsUser).To(BeNil()) // inherits the pod's uid 1000
			podSC := sts.Spec.Template.Spec.SecurityContext
			Expect(podSC.RunAsNonRoot).To(HaveValue(BeTrue()))
			Expect(podSC.RunAsUser).To(HaveValue(BeEquivalentTo(1000)))
			Expect(gateway.VolumeMounts).To(ContainElement(HaveField("MountPath", "/var/run/tailscale")))

			By("publishing nothing through tailscale serve")
			Expect(sts.Spec.Template.Spec.Volumes).NotTo(ContainElement(HaveField("Name", "config")))
			var cm corev1.ConfigMap
			err = k8sClient.Get(ctx, types.NamespacedName{Name: name + "-config", Namespace: resourceNamespace}, &cm)
			Expect(errors.IsNotFound(err)).To(BeTrue())

			By("reporting it is not ready while the node is unauthenticated")
			var updated kirocrewv1alpha1.KiroCrew
			Expect(k8sClient.Get(ctx, typeNamespacedName, &updated)).To(Succeed())
			ready := findCondition(updated.Status.Conditions, kirocrewv1alpha1.ConditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			// Nothing is served over HTTPS any more.
			Expect(updated.Status.DashboardURL).To(BeEmpty())
		})

		It("should stop outside the schedule, keep the StatefulSet, and requeue at the boundary", func() {
			var crew kirocrewv1alpha1.KiroCrew
			Expect(k8sClient.Get(ctx, typeNamespacedName, &crew)).To(Succeed())
			crew.Spec.Schedule = &kirocrewv1alpha1.ScheduleSpec{
				Up: weekdayUp, Down: weekdayDown, Timezone: tzUTC,
			}
			Expect(k8sClient.Update(ctx, &crew)).To(Succeed())

			now := time.Date(2026, 10, 12, 19, 0, 0, 0, time.UTC) // Monday evening
			r := &KiroCrewReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Now:    func() time.Time { return now },
			}
			res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(13 * time.Hour)) // until 08:00 Tuesday

			var sts appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: namePrefix + "-" + resourceName, Namespace: resourceNamespace,
			}, &sts)).To(Succeed())
			Expect(*sts.Spec.Replicas).To(BeZero())

			var updated kirocrewv1alpha1.KiroCrew
			Expect(k8sClient.Get(ctx, typeNamespacedName, &updated)).To(Succeed())
			Expect(updated.Status.Suspended).To(BeTrue())
			Expect(updated.Status.NextTransition).NotTo(BeNil())
			ready := findCondition(updated.Status.Conditions, kirocrewv1alpha1.ConditionReady)
			Expect(ready.Reason).To(Equal(kirocrewv1alpha1.ReasonOutsideSchedule))

			By("starting again once the window opens")
			now = time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&sts), &sts)).To(Succeed())
			Expect(*sts.Spec.Replicas).To(Equal(int32(1)))
		})

		It("should scale up on startedAt outside the window and stop at the next down", func() {
			var crew kirocrewv1alpha1.KiroCrew
			Expect(k8sClient.Get(ctx, typeNamespacedName, &crew)).To(Succeed())
			crew.Spec.Schedule = &kirocrewv1alpha1.ScheduleSpec{
				Up: weekdayUp, Down: weekdayDown, Timezone: tzUTC,
			}
			Expect(k8sClient.Update(ctx, &crew)).To(Succeed())

			now := time.Date(2026, 10, 12, 19, 0, 0, 0, time.UTC) // Monday evening, outside the window
			r := &KiroCrewReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Now:    func() time.Time { return now },
			}
			req := reconcile.Request{NamespacedName: typeNamespacedName}
			_, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			var sts appsv1.StatefulSet
			stsKey := types.NamespacedName{Name: namePrefix + "-" + resourceName, Namespace: resourceNamespace}
			Expect(k8sClient.Get(ctx, stsKey, &sts)).To(Succeed())
			Expect(*sts.Spec.Replicas).To(BeZero())

			By("starting it with startedAt")
			Expect(k8sClient.Get(ctx, typeNamespacedName, &crew)).To(Succeed())
			crew.Spec.StartedAt = &metav1.Time{Time: now}
			Expect(k8sClient.Update(ctx, &crew)).To(Succeed())
			res, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(23 * time.Hour)) // until 18:00 Tuesday
			Expect(k8sClient.Get(ctx, stsKey, &sts)).To(Succeed())
			Expect(*sts.Spec.Replicas).To(Equal(int32(1)))

			By("stopping at the next scheduled down")
			now = time.Date(2026, 10, 13, 18, 0, 0, 0, time.UTC)
			_, err = r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, stsKey, &sts)).To(Succeed())
			Expect(*sts.Spec.Replicas).To(BeZero())

			By("suspend overrides startedAt")
			now = time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC) // inside the window
			Expect(k8sClient.Get(ctx, typeNamespacedName, &crew)).To(Succeed())
			crew.Spec.Suspend = true
			Expect(k8sClient.Update(ctx, &crew)).To(Succeed())
			_, err = r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, stsKey, &sts)).To(Succeed())
			Expect(*sts.Spec.Replicas).To(BeZero())
		})

		It("should be idempotent across repeated reconciles", func() {
			controllerReconciler := &KiroCrewReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			for range 3 {
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespacedName,
				})
				Expect(err).NotTo(HaveOccurred())
			}
		})
	})
})

func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}
