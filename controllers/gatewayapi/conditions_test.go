package gatewayapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// condition is one status condition as the gateway controller writes it.
func condition(condType string, status metav1.ConditionStatus, observedGeneration int64) metav1.Condition {
	return metav1.Condition{Type: condType, Status: status, ObservedGeneration: observedGeneration}
}

func TestCurrentCondition_FindsTheType(t *testing.T) {
	conds := []metav1.Condition{
		condition("Accepted", metav1.ConditionFalse, 2),
		condition("Conflicted", metav1.ConditionTrue, 2),
	}

	c, found := currentCondition(conds, "Conflicted", 2)

	assert.True(t, found)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
}

func TestCurrentCondition_MissingTypeIsNotFound(t *testing.T) {
	_, found := currentCondition([]metav1.Condition{condition("Accepted", metav1.ConditionTrue, 2)}, "Conflicted", 2)

	assert.False(t, found)
}

// Right after sigillum moves a listener, the status still describes the old
// port until the gateway controller catches up.
func TestCurrentCondition_StaleConditionIsNotFound(t *testing.T) {
	_, found := currentCondition([]metav1.Condition{condition("Conflicted", metav1.ConditionTrue, 1)}, "Conflicted", 2)

	assert.False(t, found, "a verdict on the old spec says nothing about the new one")
}

// Our cached copy of the object can lag behind the gateway controller's.
func TestCurrentCondition_NewerThanOurCopyCounts(t *testing.T) {
	c, found := currentCondition([]metav1.Condition{condition("Conflicted", metav1.ConditionTrue, 3)}, "Conflicted", 2)

	assert.True(t, found)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
}

func TestCurrentCondition_NoObservedGenerationCountsAsCurrent(t *testing.T) {
	c, found := currentCondition([]metav1.Condition{condition("Conflicted", metav1.ConditionTrue, 0)}, "Conflicted", 2)

	assert.True(t, found)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
}

func TestCurrentCondition_NilListIsNotFound(t *testing.T) {
	_, found := currentCondition(nil, "Conflicted", 2)

	assert.False(t, found)
}
