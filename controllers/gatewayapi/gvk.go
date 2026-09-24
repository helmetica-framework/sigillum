// Package gatewayapi holds every Gateway API object sigillum reads or writes.
//
// The objects are unstructured, so field names are string literals with no
// compile-time check. Keeping them all in this one package means a typo has a
// single place to be caught, and the tests here are what catch it.
package gatewayapi

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	group   = "gateway.networking.k8s.io"
	version = "v1"
)

var (
	ListenerSetGVK = schema.GroupVersionKind{Group: group, Version: version, Kind: "ListenerSet"}
	TCPRouteGVK    = schema.GroupVersionKind{Group: group, Version: version, Kind: "TCPRoute"}
	HTTPRouteGVK   = schema.GroupVersionKind{Group: group, Version: version, Kind: "HTTPRoute"}
	GatewayGVK     = schema.GroupVersionKind{Group: group, Version: version, Kind: "Gateway"}
)

// allGVKs is every kind this package deals with, in registration order.
var allGVKs = []schema.GroupVersionKind{
	ListenerSetGVK,
	TCPRouteGVK,
	HTTPRouteGVK,
	GatewayGVK,
}

// Register teaches scheme about every gateway kind as unstructured.
func Register(scheme *runtime.Scheme) error {
	for _, gvk := range allGVKs {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(listKind(gvk), &unstructured.UnstructuredList{})
	}

	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Group: group, Version: version})

	return nil
}

// New returns an empty unstructured object of gvk.
//
// Always use this rather than scheme.New: the scheme stores only the Go type,
// so scheme.New hands back an object with a blank GVK.
func New(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)

	return obj
}

// NewList returns an empty unstructured list for gvk's list kind.
func NewList(gvk schema.GroupVersionKind) *unstructured.UnstructuredList {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(listKind(gvk))

	return list
}

// listKind is the list GVK that goes with gvk.
func listKind(gvk schema.GroupVersionKind) schema.GroupVersionKind {
	return gvk.GroupVersion().WithKind(gvk.Kind + "List")
}
