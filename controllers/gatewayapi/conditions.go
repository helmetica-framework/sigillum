package gatewayapi

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// currentCondition finds condType in conds.
//
// A condition older than generation was written for a spec that no longer
// exists, so it is treated as missing. An observedGeneration of 0 means it was
// not set, and the condition is taken as current.
func currentCondition(conds []metav1.Condition, condType string, generation int64) (metav1.Condition, bool) {
	for _, c := range conds {
		if c.Type != condType {
			continue
		}

		if c.ObservedGeneration != 0 && c.ObservedGeneration < generation {
			continue
		}

		return c, true
	}

	return metav1.Condition{}, false
}
