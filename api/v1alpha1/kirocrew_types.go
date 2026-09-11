package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Size is a named resource tier. Tiers mirror Kiro Crew's own cloud sizes, with
// an extra "mini" below Light for users who only run single-threaded work.
// +kubebuilder:validation:Enum=mini;light;development;power
type Size string

const (
	SizeMini        Size = "mini"
	SizeLight       Size = "light"
	SizeDevelopment Size = "development"
	SizePower       Size = "power"
)

// SizeProfile is the pod shape a Size resolves to.
//
// Memory request equals its limit (Guaranteed for memory) because memory cannot
// be safely overcommitted: a crew that bursts past a soft request is OOMKilled.
// CPU is requested well below the tier and left unlimited so the many idle crews
// on a shared cluster cost almost nothing while an active fan-out still bursts to
// the whole tier.
type SizeProfile struct {
	CPURequest string
	Memory     string
	Storage    string
}

// SizeProfiles resolves each tier. Storage is scratch space for clones and
// builds; Kiro Crew's own state (SQLite + vault + workspace) is under ~100Mi.
var SizeProfiles = map[Size]SizeProfile{
	SizeMini:        {CPURequest: "500m", Memory: "8Gi", Storage: "20Gi"},
	SizeLight:       {CPURequest: "1", Memory: "16Gi", Storage: "40Gi"},
	SizeDevelopment: {CPURequest: "2", Memory: "32Gi", Storage: "60Gi"},
	SizePower:       {CPURequest: "4", Memory: "64Gi", Storage: "80Gi"},
}

// Resolve returns the requests/limits for a tier.
func (p SizeProfile) Resolve() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(p.CPURequest),
			corev1.ResourceMemory: resource.MustParse(p.Memory),
		},
		// No CPU limit on purpose — see SizeProfile.
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse(p.Memory),
		},
	}
}

// GatewaySpec configures the Kiro Crew gateway container.
type GatewaySpec struct {
	// Image is the Kiro Crew gateway image.
	// +optional
	Image string `json:"image,omitempty"`

	// ImagePullPolicy for the gateway container.
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// Resources overrides the resolved Size tier. Set this only to escape the
	// tiers; normally leave it empty and use spec.size.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// StorageSpec configures the instance's persistent volume.
//
// This MUST be block storage. Kiro Crew keeps memory.db, memory_index.db and
// knowledge.db as SQLite in WAL mode, and SQLite documents WAL as unsupported on
// network filesystems (it needs an mmap'd shared-memory file), so EFS/NFS risks
// "database disk image is malformed" and silent corruption of a user's memory.
type StorageSpec struct {
	// Size of the PersistentVolumeClaim. Defaults to the Size tier's storage.
	// +optional
	Size *resource.Quantity `json:"size,omitempty"`

	// StorageClassName to bind. Empty uses the cluster default. Must resolve to
	// block storage (e.g. gp3), never a network filesystem.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// TailscaleSpec configures how the instance joins the tailnet.
//
// The node is deliberately USER-OWNED rather than tagged: its owner authenticates
// it once, interactively, which is what makes the tailnet's autogroup:self usable.
// That gives per-user isolation from a single static ACL grant
// (autogroup:member -> autogroup:self on tcp:8080) with no per-user ACL entries,
// so onboarding another employee needs no policy change and the operator never
// writes ACLs. A tagged node cannot do this: autogroup:self does not apply to tags.
type TailscaleSpec struct {
	// Image is the tailscale sidecar image.
	// +optional
	Image string `json:"image,omitempty"`

	// Hostname is the tailnet node name, and therefore the MagicDNS name the
	// desktop app connects to. Defaults to "kiro-crew-<instance>".
	// +optional
	Hostname string `json:"hostname,omitempty"`
}

// AWSSpec configures the instance's AWS identity.
type AWSSpec struct {
	// RoleARN is annotated onto the ServiceAccount for IRSA, giving the crew
	// (not the operator) its AWS permissions.
	// +optional
	RoleARN string `json:"roleARN,omitempty"`
}

// KiroCrewSpec defines the desired state of a Kiro Crew instance.
type KiroCrewSpec struct {
	// Owner is the tailnet identity that owns this crew, e.g. "someone@example.com".
	//
	// This is the instance's identity boundary. The crew holds this person's own
	// vaulted Kiro token, so their agent activity draws down their credits, and
	// they are the only human who should reach it. Access is enforced by the
	// tailnet: they authenticate the node, so autogroup:self admits them and
	// nobody else.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=3
	Owner string `json:"owner"`

	// Size is the resource tier for this instance.
	// +optional
	// +kubebuilder:default=light
	Size Size `json:"size,omitempty"`

	// Gateway configures the Kiro Crew gateway container.
	// +optional
	Gateway GatewaySpec `json:"gateway,omitempty"`

	// Storage configures the instance's persistent volume.
	// +optional
	Storage StorageSpec `json:"storage,omitempty"`

	// Tailscale configures how the instance joins the tailnet.
	// +optional
	Tailscale TailscaleSpec `json:"tailscale,omitempty"`

	// AWS configures the instance's AWS identity.
	// +optional
	AWS AWSSpec `json:"aws,omitempty"`

	// MCPConfigRef names a ConfigMap holding mcp-servers.json, mounted so MCP
	// endpoints can change with a helm upgrade instead of an image rebuild.
	// +optional
	MCPConfigRef string `json:"mcpConfigRef,omitempty"`
}

// Condition types reported on a KiroCrew.
const (
	// ConditionReady is true once the instance is serving its owner.
	ConditionReady = "Ready"
	// ConditionTailnetJoined is true once the node has joined the tailnet. It
	// stays false while the node is waiting for its owner's one-time interactive
	// authentication, with the login URL in status.tailnetLoginURL.
	ConditionTailnetJoined = "TailnetJoined"
	// ConditionKiroAuthenticated is true once the crew holds a Kiro token. Like
	// the tailnet join this needs a one-time human approval — the device-code
	// flow, approved from the owner's own browser — so it cannot be provisioned.
	ConditionKiroAuthenticated = "KiroAuthenticated"
)

// KiroCrewStatus reports the observed state of a Kiro Crew instance.
type KiroCrewStatus struct {
	// Conditions holds the latest observations.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// TailnetLoginURL is the URL the owner opens to authenticate this node, once.
	//
	// Surfaced here to break a bootstrap cycle: the node is not on the tailnet
	// until it is authenticated, so the URL cannot be delivered over the tailnet
	// it is trying to join. It is cleared once the node is joined.
	// +optional
	TailnetLoginURL string `json:"tailnetLoginURL,omitempty"`

	// DashboardURL is the tailnet HTTPS address of the dashboard, published by
	// tailscale serve. This is what the Kiro Crew desktop app connects to via
	// New Connection Window; the app requires https for a non-loopback host.
	// +optional
	DashboardURL string `json:"dashboardURL,omitempty"`

	// ObservedGeneration is the .metadata.generation this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=kc
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=".spec.owner"
// +kubebuilder:printcolumn:name="Size",type=string,JSONPath=".spec.size"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Dashboard",type=string,JSONPath=".status.dashboardURL"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// KiroCrew is a single-owner Kiro Crew instance.
type KiroCrew struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KiroCrewSpec   `json:"spec,omitempty"`
	Status KiroCrewStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KiroCrewList contains a list of KiroCrew.
type KiroCrewList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KiroCrew `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(SchemeGroupVersion, &KiroCrew{}, &KiroCrewList{})
		return nil
	})
}
