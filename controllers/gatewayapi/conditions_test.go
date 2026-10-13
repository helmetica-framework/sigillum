package gatewayapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// condition is one status condition as the gateway controller writes it.
func condition(condType, status string, observedGeneration int64) map[string]any {
	return map[string]any{
		"type":               condType,
		"status":             status,
		"observedGeneration": observedGeneration,
	}
}

func TestCurrentCondition_FindsTheType(t *testing.T) {
	conds := []any{
		condition("Accepted", "False", 2),
		condition("Conflicted", "True", 2),
	}

	status, found := currentCondition(conds, "Conflicted", 2)

	assert.True(t, found)
	assert.Equal(t, "True", status)
}

func TestCurrentCondition_MissingTypeIsNotFound(t *testing.T) {
	_, found := currentCondition([]any{condition("Accepted", "True", 2)}, "Conflicted", 2)

	assert.False(t, found)
}

// Right after sigillum moves a listener, the status still describes the old
// port until the gateway controller catches up.
func TestCurrentCondition_StaleConditionIsNotFound(t *testing.T) {
	_, found := currentCondition([]any{condition("Conflicted", "True", 1)}, "Conflicted", 2)

	assert.False(t, found, "a verdict on the old spec says nothing about the new one")
}

// Our cached copy of the object can lag behind the gateway controller's.
func TestCurrentCondition_NewerThanOurCopyCounts(t *testing.T) {
	status, found := currentCondition([]any{condition("Conflicted", "True", 3)}, "Conflicted", 2)

	assert.True(t, found)
	assert.Equal(t, "True", status)
}

func TestCurrentCondition_NoObservedGenerationCountsAsCurrent(t *testing.T) {
	conds := []any{map[string]any{"type": "Conflicted", "status": "True"}}

	status, found := currentCondition(conds, "Conflicted", 2)

	assert.True(t, found)
	assert.Equal(t, "True", status)
}

func TestCurrentCondition_MalformedEntriesAreSkipped(t *testing.T) {
	conds := []any{
		"not a map",
		map[string]any{"status": "True"},
		condition("Conflicted", "True", 2),
	}

	status, found := currentCondition(conds, "Conflicted", 2)

	assert.True(t, found)
	assert.Equal(t, "True", status)
}

func TestCurrentCondition_NilListIsNotFound(t *testing.T) {
	_, found := currentCondition(nil, "Conflicted", 2)

	assert.False(t, found)
}
