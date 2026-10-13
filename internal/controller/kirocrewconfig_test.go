package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kirocrewv1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

var _ = Describe("KiroCrewConfig", func() {
	ctx := context.Background()
	const (
		ns       = "default"
		gwOne    = "registry.example.com/gateway:1.0.0"
		gwTwo    = "registry.example.com/gateway:1.1.0"
		tsImage  = "registry.example.com/tailscale:1.2.3"
		ownImage = "registry.example.com/own:9.9.9"
	)

	newConfig := func(spec kirocrewv1alpha1.KiroCrewConfigSpec) *kirocrewv1alpha1.KiroCrewConfig {
		GinkgoHelper()
		cfg := &kirocrewv1alpha1.KiroCrewConfig{ObjectMeta: metav1.ObjectMeta{Name: kirocrewv1alpha1.ConfigName}, Spec: spec}
		Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
		return cfg
	}

	AfterEach(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &kirocrewv1alpha1.KiroCrewConfig{
			ObjectMeta: metav1.ObjectMeta{Name: kirocrewv1alpha1.ConfigName},
		}))).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &kirocrewv1alpha1.KiroCrew{}, client.InNamespace(ns))).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &kirocrewv1alpha1.KiroCrewAccount{})).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &kirocrewv1alpha1.KiroCrewRequest{}, client.InNamespace(ns))).To(Succeed())
	})

	It("is only valid under the name default", func() {
		err := k8sClient.Create(ctx, &kirocrewv1alpha1.KiroCrewConfig{ObjectMeta: metav1.ObjectMeta{Name: "other"}})
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
		Expect(err).To(MatchError(ContainSubstring("the only allowed name is default")))
	})

	It("defaults the sizes and rejects a default outside the allowed ones", func() {
		cfg := newConfig(kirocrewv1alpha1.KiroCrewConfigSpec{})
		Expect(cfg.Spec.AllowedSizes).To(HaveLen(4))
		Expect(cfg.Spec.DefaultSize).To(Equal(kirocrewv1alpha1.SizeLight))

		cfg.Spec.AllowedSizes = []kirocrewv1alpha1.Size{kirocrewv1alpha1.SizeMini}
		err := k8sClient.Update(ctx, cfg)
		Expect(err).To(MatchError(ContainSubstring("defaultSize must be one of allowedSizes")))
	})

	It("EnsureConfig creates the default once", func() {
		Expect(EnsureConfig(ctx, k8sClient)).To(Succeed())
		Expect(EnsureConfig(ctx, k8sClient)).To(Succeed())
		cfg := &kirocrewv1alpha1.KiroCrewConfig{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: kirocrewv1alpha1.ConfigName}, cfg)).To(Succeed())
		Expect(cfg.Spec.DefaultSize).To(Equal(kirocrewv1alpha1.SizeLight))
	})

	It("rolls the crew's images and MCP ref from the config on the next reconcile", func() {
		cfg := newConfig(kirocrewv1alpha1.KiroCrewConfigSpec{
			Gateway:      kirocrewv1alpha1.ImageRef{Image: gwOne},
			Tailscale:    kirocrewv1alpha1.ImageRef{Image: tsImage},
			MCPConfigRef: "shared-mcp",
		})
		Expect(k8sClient.Create(ctx, &kirocrewv1alpha1.KiroCrew{
			ObjectMeta: metav1.ObjectMeta{Name: "cfg-crew", Namespace: ns},
			Spec:       kirocrewv1alpha1.KiroCrewSpec{Owner: testOwner},
		})).To(Succeed())
		r := &KiroCrewReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), TailnetDomain: "example-tailnet.ts.net"}
		key := types.NamespacedName{Name: "cfg-crew", Namespace: ns}
		reconcileOnce := func() appsv1.StatefulSet {
			GinkgoHelper()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			var sts appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: namePrefix + "-cfg-crew", Namespace: ns}, &sts)).To(Succeed())
			return sts
		}

		sts := reconcileOnce()
		Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal(gwOne))
		Expect(sts.Spec.Template.Spec.Containers[1].Image).To(Equal(tsImage))
		Expect(sts.Spec.Template.Spec.Volumes).To(ContainElement(HaveField("Name", "mcp-config")))

		cfg.Spec.Gateway.Image = gwTwo
		Expect(k8sClient.Update(ctx, cfg)).To(Succeed())
		Expect(reconcileOnce().Spec.Template.Spec.Containers[0].Image).To(Equal(gwTwo))

		By("letting the crew's own image win")
		var crew kirocrewv1alpha1.KiroCrew
		Expect(k8sClient.Get(ctx, key, &crew)).To(Succeed())
		crew.Spec.Gateway.Image = ownImage
		Expect(k8sClient.Update(ctx, &crew)).To(Succeed())
		Expect(reconcileOnce().Spec.Template.Spec.Containers[0].Image).To(Equal(ownImage))
	})

	It("marks a request for a size outside allowedSizes Invalid, then recovers when it is allowed", func() {
		Expect(k8sClient.Create(ctx, &kirocrewv1alpha1.KiroCrewAccount{
			ObjectMeta: metav1.ObjectMeta{Name: "cfg-alice"},
			Spec:       kirocrewv1alpha1.KiroCrewAccountSpec{Email: reqRequester, Role: kirocrewv1alpha1.RoleUser},
		})).To(Succeed())
		cfg := newConfig(kirocrewv1alpha1.KiroCrewConfigSpec{
			AllowedSizes: []kirocrewv1alpha1.Size{kirocrewv1alpha1.SizeMini, kirocrewv1alpha1.SizeLight},
		})
		req := &kirocrewv1alpha1.KiroCrewRequest{
			ObjectMeta: metav1.ObjectMeta{Name: "cfg-req", Namespace: ns},
			Spec:       kirocrewv1alpha1.KiroCrewRequestSpec{Requester: reqRequester, Size: kirocrewv1alpha1.SizePower},
		}
		Expect(k8sClient.Create(ctx, req)).To(Succeed())
		r := &KiroCrewRequestReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		phase := func() kirocrewv1alpha1.RequestPhase {
			GinkgoHelper()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(req)})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(req), req)).To(Succeed())
			return req.Status.Phase
		}

		Expect(phase()).To(Equal(kirocrewv1alpha1.PhaseInvalid))
		Expect(req.Status.Conditions).To(ContainElement(HaveField("Reason", kirocrewv1alpha1.ReasonSizeNotAllowed)))

		cfg.Spec.AllowedSizes = append(cfg.Spec.AllowedSizes, kirocrewv1alpha1.SizePower)
		Expect(k8sClient.Update(ctx, cfg)).To(Succeed())
		Expect(phase()).To(Equal(kirocrewv1alpha1.PhasePending))
	})
})
