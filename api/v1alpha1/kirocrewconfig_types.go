package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ConfigName is the only name a KiroCrewConfig may have.
const ConfigName = "default"

// ImageRef overrides one container image.
type ImageRef struct {
	// Image is the full image reference, including its tag.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^\S*$`
	Image string `json:"image,omitempty"`
}

// KiroCrewConfigSpec holds the product defaults admins tune day to day.
// +kubebuilder:validation:XValidation:rule="self.defaultSize in self.allowedSizes",message="defaultSize must be one of allowedSizes"
type KiroCrewConfigSpec struct {
	// AllowedSizes are the tiers the request form offers and requests may use.
	// +optional
	// +kubebuilder:default={"mini","light","development","power"}
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=4
	// +listType=set
	AllowedSizes []Size `json:"allowedSizes,omitempty"`

	// DefaultSize is preselected on the request form.
	// +optional
	// +kubebuilder:default=light
	DefaultSize Size `json:"defaultSize,omitempty"`

	// Gateway overrides the gateway image of every crew that sets none.
	// +optional
	Gateway ImageRef `json:"gateway,omitzero"`

	// MCPConfigRef names the ConfigMap used by crews whose own mcpConfigRef is empty.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	MCPConfigRef string `json:"mcpConfigRef,omitempty"`

	// MCPConfigs are the ConfigMap names, in the crew namespace, an admin may
	// choose from when setting a crew's mcpConfigRef in the console.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	MCPConfigs []string `json:"mcpConfigs,omitempty"`
}

// Sizes returns the allowed tiers and the default one, falling back to every
// tier and Light when the config is absent or unset.
func (s KiroCrewConfigSpec) Sizes() ([]Size, Size) {
	allowed := s.AllowedSizes
	if len(allowed) == 0 {
		allowed = []Size{SizeMini, SizeLight, SizeDevelopment, SizePower}
	}
	def := s.DefaultSize
	if def == "" {
		def = SizeLight
	}
	return allowed, def
}

// KiroCrewConfigStatus defines the observed state of KiroCrewConfig.
type KiroCrewConfigStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'default'",message="the only allowed name is default"

// KiroCrewConfig is the Schema for the kirocrewconfigs API
type KiroCrewConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of KiroCrewConfig
	// +required
	Spec KiroCrewConfigSpec `json:"spec"`

	// status defines the observed state of KiroCrewConfig
	// +optional
	Status KiroCrewConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// KiroCrewConfigList contains a list of KiroCrewConfig
type KiroCrewConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []KiroCrewConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &KiroCrewConfig{}, &KiroCrewConfigList{})
		return nil
	})
}
