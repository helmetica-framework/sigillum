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

// Protocol is what an exposure carries.
// +kubebuilder:validation:Enum=HTTP;HTTPS;TCP
type Protocol string

const (
	ProtocolHTTP  Protocol = "HTTP"
	ProtocolHTTPS Protocol = "HTTPS"
	ProtocolTCP   Protocol = "TCP"
)

// Backend names the in-namespace service an exposure points at.
type Backend struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Service string `json:"service"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
}

// Exposure asks for one way into the sealed namespace from outside the cluster.
//
// There is deliberately no port field. Sigillum allocates TCP ports itself and
// reports them in status; letting a user pick one would reintroduce the clash
// this controller exists to prevent.
type Exposure struct {
	// Name identifies the exposure within this Seal. It becomes the listener
	// and route name, so it has to be a DNS label.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Empty means the class named after this exposure's protocol, lowercased.
	// +optional
	ExposureClass string `json:"exposureClass,omitempty"`

	Protocol Protocol `json:"protocol"`

	// Empty means no subdomain label. HTTP and HTTPS only.
	// +optional
	Subdomain string `json:"subdomain,omitempty"`

	Backend Backend `json:"backend"`
}

// ExposureStatus reports what an Exposure turned into.
type ExposureStatus struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// +optional
	Class string `json:"class,omitempty"`

	Ready bool `json:"ready"`

	// +optional
	Message string `json:"message,omitempty"`

	// --- HTTP and HTTPS ---

	// +optional
	Hostname string `json:"hostname,omitempty"`

	// --- TCP ---

	// +optional
	Shard string `json:"shard,omitempty"`

	// +optional
	Address string `json:"address,omitempty"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`

	// --- Both ---

	// +optional
	URL string `json:"url,omitempty"`

	// ConflictCount stops a re-allocation loop when the gateway controller
	// keeps rejecting the port it was given.
	// +kubebuilder:validation:Minimum=0
	// +optional
	ConflictCount int32 `json:"conflictCount,omitempty"`
}

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

	// Expose asks for ways into this namespace from outside the cluster.
	//
	// listType=map makes the API server reject duplicate names and makes
	// server-side apply merge per entry rather than replacing the whole list.
	// +listType=map
	// +listMapKey=name
	// +optional
	Expose []Exposure `json:"expose,omitempty"`
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

	// Exposures reports one entry per Expose entry, in spec order. Every
	// requested exposure appears here from the first reconcile, even before its
	// class resolves: an empty list would read as "nothing was requested".
	//
	// +listType=map
	// +listMapKey=name
	// +optional
	Exposures []ExposureStatus `json:"exposures,omitempty"`
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
