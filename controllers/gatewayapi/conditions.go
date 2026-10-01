package gatewayapi

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// findCurrentCondition finds condType in a raw status condition list.
//
// A condition older than generation was written for a spec that no longer
// exists, so it is treated as missing. A condition with no observedGeneration
// is taken as current.
func findCurrentCondition(conditions []any, condType string, generation int64) (map[string]any, bool) {
	for _, raw := range conditions {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}

		t, _, _ := unstructured.NestedString(c, "type")
		if t != condType {
			continue
		}

		observed, found, _ := unstructured.NestedInt64(c, "observedGeneration")
		if found && observed < generation {
			continue
		}

		return c, true
	}

	return nil, false
}

// currentCondition is findCurrentCondition, returning only the status.
func currentCondition(conditions []any, condType string, generation int64) (string, bool) {
	c, found := findCurrentCondition(conditions, condType, generation)
	if !found {
		return "", false
	}

	status, _, _ := unstructured.NestedString(c, "status")
	return status, true
}
