package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kirocrewv1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

// +kubebuilder:rbac:groups=kirocrew.lsdopen.io,resources=kirocrewconfigs,verbs=get;list;watch;create

// loadConfigSpec returns the singleton's spec, or the zero spec (compiled
// defaults) while it does not exist.
func loadConfigSpec(ctx context.Context, c client.Reader) (kirocrewv1alpha1.KiroCrewConfigSpec, error) {
	cfg := &kirocrewv1alpha1.KiroCrewConfig{}
	err := c.Get(ctx, client.ObjectKey{Name: kirocrewv1alpha1.ConfigName}, cfg)
	if apierrors.IsNotFound(err) {
		return kirocrewv1alpha1.KiroCrewConfigSpec{}, nil
	}
	return cfg.Spec, err
}

// EnsureConfig creates the empty `default` config if it is missing, so the
// console, which may only update it, always has one to edit. The API server
// fills in the defaults.
func EnsureConfig(ctx context.Context, c client.Client) error {
	cfg := &kirocrewv1alpha1.KiroCrewConfig{ObjectMeta: metav1.ObjectMeta{Name: kirocrewv1alpha1.ConfigName}}
	return client.IgnoreAlreadyExists(c.Create(ctx, cfg))
}

// crewsForConfig re-queues every crew, so a config change rolls them promptly.
func (r *KiroCrewReconciler) crewsForConfig(ctx context.Context, _ client.Object) []reconcile.Request {
	crews := &kirocrewv1alpha1.KiroCrewList{}
	if err := r.List(ctx, crews); err != nil {
		return nil
	}
	out := make([]reconcile.Request, 0, len(crews.Items))
	for i := range crews.Items {
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&crews.Items[i])})
	}
	return out
}
