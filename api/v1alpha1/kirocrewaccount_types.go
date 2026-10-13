package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Role is what an account may do on the platform.
// +kubebuilder:validation:Enum=admin;manager;user
type Role string

const (
	RoleAdmin   Role = "admin"
	RoleManager Role = "manager"
	RoleUser    Role = "user"
)

// KiroCrewAccountSpec defines one person on the platform.
type KiroCrewAccountSpec struct {
	// Email is the person's Google login.
	// +required
	// +kubebuilder:validation:Pattern=`^[^@\s]+@[^@\s]+$`
	Email string `json:"email"`

	// Role is the person's platform role.
	// +required
	Role Role `json:"role"`

	// Manager is the email of this person's manager, who may approve their requests.
	// +optional
	// +kubebuilder:validation:Pattern=`^[^@\s]+@[^@\s]+$`
	Manager string `json:"manager,omitempty"`

	// DisplayName is a human-readable name for the console.
	// +optional
	DisplayName string `json:"displayName,omitempty"`
}

// KiroCrewAccountStatus defines the observed state of KiroCrewAccount.
type KiroCrewAccountStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the KiroCrewAccount resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Email",type=string,JSONPath=".spec.email"
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=".spec.role"
// +kubebuilder:printcolumn:name="Manager",type=string,JSONPath=".spec.manager"

// KiroCrewAccount is the Schema for the kirocrewaccounts API
type KiroCrewAccount struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of KiroCrewAccount
	// +required
	Spec KiroCrewAccountSpec `json:"spec"`

	// status defines the observed state of KiroCrewAccount
	// +optional
	Status KiroCrewAccountStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// KiroCrewAccountList contains a list of KiroCrewAccount
type KiroCrewAccountList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []KiroCrewAccount `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &KiroCrewAccount{}, &KiroCrewAccountList{})
		return nil
	})
}
