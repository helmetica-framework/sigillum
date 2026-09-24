package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ExposureType is how an ExposureClass gets traffic into the cluster.
//
// Ingress and LoadBalancer are out of scope, so the enum does not offer them.
// +kubebuilder:validation:Enum=Gateway
type ExposureType string

const (
	ExposureTypeGateway ExposureType = "Gateway"
)

// TLSMode is who terminates TLS for an exposure.
// +kubebuilder:validation:Enum=GatewayTerminated;PerExposure
type TLSMode string

const (
	// TLSModeGatewayTerminated leaves TLS to the gateway's own certificate.
	TLSModeGatewayTerminated TLSMode = "GatewayTerminated"

	// TLSModePerExposure issues a certificate per exposure. Not implemented
	// yet: the controller rejects it with a named error.
	TLSModePerExposure TLSMode = "PerExposure"
)

// PortRange is the inclusive range a TCP exposure may be allocated from.
// +kubebuilder:validation:XValidation:rule="self.to >= self.from",message="to must not be below from"
type PortRange struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	From int32 `json:"from"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	To int32 `json:"to"`
}

// GatewaySelector finds the gateway pods that must be allowed through the
// NetworkPolicy.
type GatewaySelector struct {
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// +optional
	PodSelector *metav1.LabelSelector `json:"podSelector,omitempty"`
}

// ParentRef names the Gateway an HTTP exposure attaches to.
type ParentRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// IssuerRef names the cert-manager issuer for TLSModePerExposure.
type IssuerRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// +kubebuilder:validation:Enum=Issuer;ClusterIssuer
	// +optional
	Kind string `json:"kind,omitempty"`
}

// ExposureTLS says who terminates TLS and, when sigillum does, with what.
type ExposureTLS struct {
	Mode TLSMode `json:"mode"`

	// +optional
	IssuerRef *IssuerRef `json:"issuerRef,omitempty"`
}

// ExposureClassSpec describes how one protocol reaches the outside world.
type ExposureClassSpec struct {
	Type ExposureType `json:"type"`

	// +kubebuilder:validation:MinItems=1
	// +listType=set
	Supports []Protocol `json:"supports"`

	// --- HTTP and HTTPS ---

	// +optional
	ParentRef *ParentRef `json:"parentRef,omitempty"`

	// +optional
	Domain string `json:"domain,omitempty"`

	// +optional
	TLS *ExposureTLS `json:"tls,omitempty"`

	// --- TCP ---

	// +optional
	ShardSelector *metav1.LabelSelector `json:"shardSelector,omitempty"`

	// +optional
	PortRange *PortRange `json:"portRange,omitempty"`

	// MaxListenersPerShard is operator policy. 0 means the API's own limit of
	// 64, not unlimited.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=64
	// +optional
	MaxListenersPerShard int32 `json:"maxListenersPerShard,omitempty"`

	// --- Both ---

	// +optional
	GatewaySelector *GatewaySelector `json:"gatewaySelector,omitempty"`
}

// ExposureClassStatus is reserved. Nothing writes it yet.
type ExposureClassStatus struct{}

// ExposureClass describes how one protocol reaches the outside world.
//
// An exposure that names no class falls back to the class named after its
// protocol, lowercased: TCP looks for "tcp", HTTPS for "https". There is no
// default flag, so two classes can never both claim a protocol.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Supports",type=string,JSONPath=`.spec.supports`
type ExposureClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ExposureClassSpec   `json:"spec,omitempty"`
	Status ExposureClassStatus `json:"status,omitempty"`
}

// ExposureClassList contains a list of ExposureClass.
// +kubebuilder:object:root=true
type ExposureClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ExposureClass `json:"items"`
}

func init() { SchemeBuilder.Register(&ExposureClass{}, &ExposureClassList{}) }
