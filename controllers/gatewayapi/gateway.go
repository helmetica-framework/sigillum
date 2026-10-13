package gatewayapi

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// GatewayAddress reads the first address off a Gateway's status. False means
// the gateway controller has not assigned one yet.
func GatewayAddress(obj *unstructured.Unstructured) (string, bool) {
	if obj == nil {
		return "", false
	}

	addresses, _, _ := unstructured.NestedSlice(obj.Object, "status", "addresses")

	if len(addresses) == 0 {
		return "", false
	}

	first, ok := addresses[0].(map[string]any)
	if !ok {
		return "", false
	}

	value, _, _ := unstructured.NestedString(first, "value")
	return value, value != ""
}
