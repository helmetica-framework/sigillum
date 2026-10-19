// Package gatewayapi holds every Gateway API object sigillum reads or writes.
package gatewayapi

import (
	"k8s.io/apimachinery/pkg/runtime"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Register adds the Gateway API v1 types to scheme.
func Register(scheme *runtime.Scheme) error {
	return gwv1.Install(scheme)
}
