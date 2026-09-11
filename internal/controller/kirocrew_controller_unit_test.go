package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kirocrewv1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

const (
	testOwner         = "seagyn@lsdopen.io"
	reasonServing     = "Serving"
	reasonPodNotReady = "PodNotReady"
)

func crewWith(spec kirocrewv1alpha1.KiroCrewSpec) *kirocrewv1alpha1.KiroCrew {
	return &kirocrewv1alpha1.KiroCrew{
		ObjectMeta: metav1.ObjectMeta{Name: "seagyn", Namespace: "kiro-crew"},
		Spec:       spec,
	}
}

func TestResolveSizeDefaultsToLight(t *testing.T) {
	// An unset size must not resolve to an empty pod shape.
	resources, storage := resolveSize(crewWith(kirocrewv1alpha1.KiroCrewSpec{
		Owner: testOwner,
	}))

	light := kirocrewv1alpha1.SizeProfiles[kirocrewv1alpha1.SizeLight]
	if got := resources.Requests.Memory().String(); got != light.Memory {
		t.Errorf("memory request = %s, want %s", got, light.Memory)
	}
	if got := storage.String(); got != light.Storage {
		t.Errorf("storage = %s, want %s", got, light.Storage)
	}
}

func TestResolveSizeUnknownTierFallsBack(t *testing.T) {
	// A tier the CRD enum does not cover must still produce a usable shape
	// rather than zero resources.
	resources, _ := resolveSize(crewWith(kirocrewv1alpha1.KiroCrewSpec{
		Owner: testOwner,
		Size:  kirocrewv1alpha1.Size("enormous"),
	}))

	want := kirocrewv1alpha1.SizeProfiles[kirocrewv1alpha1.SizeLight].Memory
	if got := resources.Requests.Memory().String(); got != want {
		t.Errorf("memory request = %s, want fallback %s", got, want)
	}
}

// Memory cannot be safely overcommitted: a crew that bursts past a soft request
// is OOMKilled, so every tier must pin request == limit.
func TestEveryTierPinsMemoryRequestToLimit(t *testing.T) {
	for size, profile := range kirocrewv1alpha1.SizeProfiles {
		resources := profile.Resolve()
		req := resources.Requests[corev1.ResourceMemory]
		lim := resources.Limits[corev1.ResourceMemory]
		if req.Cmp(lim) != 0 {
			t.Errorf("%s: memory request %s != limit %s", size, req.String(), lim.String())
		}
	}
}

// CPU is deliberately left unlimited so idle crews cost almost nothing while an
// active fan-out can burst to the whole tier.
func TestNoTierSetsACPULimit(t *testing.T) {
	for size, profile := range kirocrewv1alpha1.SizeProfiles {
		resources := profile.Resolve()
		if _, found := resources.Limits[corev1.ResourceCPU]; found {
			t.Errorf("%s: unexpected CPU limit; CPU must stay burstable", size)
		}
	}
}

func TestResourcesOverrideBeatsTier(t *testing.T) {
	override := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("3Gi")},
	}
	resources, _ := resolveSize(crewWith(kirocrewv1alpha1.KiroCrewSpec{
		Owner:   testOwner,
		Size:    kirocrewv1alpha1.SizePower,
		Gateway: kirocrewv1alpha1.GatewaySpec{Resources: &override},
	}))

	if got := resources.Requests.Memory().String(); got != "3Gi" {
		t.Errorf("memory request = %s, want the override 3Gi", got)
	}
}

func TestStorageSizeOverrideBeatsTier(t *testing.T) {
	want := resource.MustParse("5Gi")
	_, storage := resolveSize(crewWith(kirocrewv1alpha1.KiroCrewSpec{
		Owner:   testOwner,
		Size:    kirocrewv1alpha1.SizeMini,
		Storage: kirocrewv1alpha1.StorageSpec{Size: &want},
	}))

	if storage.Cmp(want) != 0 {
		t.Errorf("storage = %s, want the override %s", storage.String(), want.String())
	}
}

func TestNamesArePrefixed(t *testing.T) {
	// Every generated object and the tailnet hostname share one prefix.
	if got := instanceName(crewWith(kirocrewv1alpha1.KiroCrewSpec{})); got != "kiro-crew-seagyn" {
		t.Errorf("instanceName = %q, want %q", got, "kiro-crew-seagyn")
	}
}

func TestTailnetLoginURLPatternTakesTheLastMatch(t *testing.T) {
	// A restart can leave an already-consumed URL earlier in the log tail, so the
	// most recent one is the one worth showing the owner.
	logs := `
some noise
To authenticate, visit: https://login.tailscale.com/a/OLDOLDOLD
more noise
To authenticate, visit: https://login.tailscale.com/a/NEWNEWNEW
`
	matches := tailnetLoginURLPattern.FindAllString(logs, -1)
	if len(matches) != 2 {
		t.Fatalf("found %d URLs, want 2", len(matches))
	}
	if got := matches[len(matches)-1]; got != "https://login.tailscale.com/a/NEWNEWNEW" {
		t.Errorf("last match = %q, want the newest URL", got)
	}
}

func TestTailnetLoginURLPatternIgnoresUnrelatedURLs(t *testing.T) {
	if tailnetLoginURLPattern.MatchString("https://example.com/a/nope") {
		t.Error("pattern matched a non-Tailscale URL")
	}
}

func TestUpsertConditionReplacesInPlace(t *testing.T) {
	conditions := []metav1.Condition{
		{Type: kirocrewv1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: reasonPodNotReady},
	}
	upsertCondition(&conditions, metav1.Condition{
		Type: kirocrewv1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: reasonServing,
	})

	if len(conditions) != 1 {
		t.Fatalf("got %d conditions, want the existing one replaced", len(conditions))
	}
	if conditions[0].Status != metav1.ConditionTrue || conditions[0].Reason != reasonServing {
		t.Errorf("condition not updated: %+v", conditions[0])
	}
}

// A flapping status must not rewrite LastTransitionTime while the status is
// unchanged, or "how long has this been ready" becomes meaningless.
func TestUpsertConditionKeepsTransitionTimeWhenStatusUnchanged(t *testing.T) {
	original := metav1.NewTime(metav1.Now().Add(-1))
	conditions := []metav1.Condition{{
		Type:               kirocrewv1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             reasonServing,
		LastTransitionTime: original,
	}}
	upsertCondition(&conditions, metav1.Condition{
		Type: kirocrewv1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: reasonServing,
	})

	if !conditions[0].LastTransitionTime.Equal(&original) {
		t.Errorf("LastTransitionTime moved on an unchanged status")
	}
}

func TestUpsertConditionAppendsNewType(t *testing.T) {
	conditions := []metav1.Condition{
		{Type: kirocrewv1alpha1.ConditionReady, Status: metav1.ConditionTrue},
	}
	upsertCondition(&conditions, metav1.Condition{
		Type: kirocrewv1alpha1.ConditionTailnetJoined, Status: metav1.ConditionFalse,
	})

	if len(conditions) != 2 {
		t.Fatalf("got %d conditions, want 2", len(conditions))
	}
}
