package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type SealPhase string

const (
	SealPhasePending SealPhase = "Pending"
	SealPhaseReady   SealPhase = "Ready"
	SealPhaseFailed  SealPhase = "Failed"
)

// SealSpec describes who may reach the sealed namespace.
type SealSpec struct {
	// AllowedNamespaces names the namespaces that may reach this one, on top of
	// the claim namespace the seal's own namespace is annotated with. Ignored
	// when AllowAllNamespaces is true.
	// +optional
	AllowedNamespaces []string `json:"allowedNamespaces,omitempty"`

	// AllowAllNamespaces allows access from every namespace in the cluster,
	// effectively disabling the NetworkPolicy. It overrides AllowedNamespaces
	// and the claim namespace. Traffic from outside the cluster stays blocked.
	// +optional
	AllowAllNamespaces *bool `json:"allowAllNamespaces,omitempty"`
}

type SealStatus struct {
	// +optional
	Phase SealPhase `json:"phase,omitempty"`
	// ObservedGeneration is the spec generation the phase was computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Message explains a Failed phase.
	// +optional
	Message string `json:"message,omitempty"`
}

// Seal is sigillum's placeholder resource.
// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="All Namespaces",type=boolean,JSONPath=`.spec.allowAllNamespaces`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`
type Seal struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SealSpec   `json:"spec,omitempty"`
	Status SealStatus `json:"status,omitempty"`
}

// SealList contains a list of Seal.
// +kubebuilder:object:root=true
type SealList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Seal `json:"items"`
}

func init() { SchemeBuilder.Register(&Seal{}, &SealList{}) }
