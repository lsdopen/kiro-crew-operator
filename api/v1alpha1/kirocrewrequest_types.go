package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Verdict is an approver's decision on a request.
// +kubebuilder:validation:Enum=approved;denied
type Verdict string

const (
	VerdictApproved Verdict = "approved"
	VerdictDenied   Verdict = "denied"
)

// RequestPhase is where a request is in its lifecycle.
// +kubebuilder:validation:Enum=Pending;Approved;Denied;Provisioned;Invalid
type RequestPhase string

const (
	PhasePending     RequestPhase = "Pending"
	PhaseApproved    RequestPhase = "Approved"
	PhaseDenied      RequestPhase = "Denied"
	PhaseProvisioned RequestPhase = "Provisioned"
	PhaseInvalid     RequestPhase = "Invalid"
)

// Decision is an approver's verdict.
type Decision struct {
	// Verdict is approved or denied.
	// +required
	Verdict Verdict `json:"verdict"`

	// By is the approver's email.
	// +required
	// +kubebuilder:validation:Pattern=`^[^@\s]+@[^@\s]+$`
	By string `json:"by"`
}

// KiroCrewRequestSpec is a person's ask for a crew.
type KiroCrewRequestSpec struct {
	// Requester is the email of the person the crew is for. It must match a KiroCrewAccount.
	// +required
	// +kubebuilder:validation:Pattern=`^[^@\s]+@[^@\s]+$`
	Requester string `json:"requester"`

	// Size is the requested resource tier.
	// +optional
	// +kubebuilder:default=light
	Size Size `json:"size,omitempty"`

	// Schedule is the requested start/stop window.
	// +optional
	Schedule *ScheduleSpec `json:"schedule,omitempty"`

	// Reason is why the crew is wanted.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Decision is the approver's verdict, written by the console.
	// +optional
	Decision *Decision `json:"decision,omitempty"`
}

// Condition types and reasons reported on a KiroCrewRequest.
const (
	// ConditionRequestValid is false while the request cannot be acted on.
	ConditionRequestValid = "Valid"

	ReasonValid                = "Valid"
	ReasonUnknownRequester     = "UnknownRequester"
	ReasonUnauthorisedApprover = "UnauthorisedApprover"
	ReasonCrewNameTaken        = "CrewNameTaken"
)

// KiroCrewRequestStatus defines the observed state of KiroCrewRequest.
type KiroCrewRequestStatus struct {
	// Phase is the request's lifecycle stage.
	// +optional
	Phase RequestPhase `json:"phase,omitempty"`

	// DecidedBy is the approver whose decision was accepted.
	// +optional
	DecidedBy string `json:"decidedBy,omitempty"`

	// DecidedAt is when the decision was first accepted.
	// +optional
	DecidedAt *metav1.Time `json:"decidedAt,omitempty"`

	// CrewRef is the name of the KiroCrew created for this request.
	// +optional
	CrewRef string `json:"crewRef,omitempty"`

	// conditions represent the current state of the KiroCrewRequest resource.
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
// +kubebuilder:printcolumn:name="Requester",type=string,JSONPath=".spec.requester"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Crew",type=string,JSONPath=".status.crewRef"

// KiroCrewRequest is the Schema for the kirocrewrequests API
type KiroCrewRequest struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of KiroCrewRequest
	// +required
	Spec KiroCrewRequestSpec `json:"spec"`

	// status defines the observed state of KiroCrewRequest
	// +optional
	Status KiroCrewRequestStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// KiroCrewRequestList contains a list of KiroCrewRequest
type KiroCrewRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []KiroCrewRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &KiroCrewRequest{}, &KiroCrewRequestList{})
		return nil
	})
}
