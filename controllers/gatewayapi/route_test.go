package gatewayapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func redisBackend() Backend {
	return Backend{Service: "redis", Port: 6379}
}

// builtRoute is BuildTCPRoute over the common case.
func builtRoute(t *testing.T) *unstructured.Unstructured {
	t.Helper()

	obj, err := BuildTCPRoute("networkconfig", "redis-7f3", "direct", redisBackend(), sealOwner())
	require.NoError(t, err)
	require.NotNil(t, obj)

	return obj
}

func TestBuildTCPRoute_ShapeAndNames(t *testing.T) {
	obj := builtRoute(t)

	assert.Equal(t, TCPRouteGVK, obj.GroupVersionKind())
	assert.Equal(t, "networkconfig-direct", obj.GetName())
	assert.Equal(t, "redis-7f3", obj.GetNamespace())
}

// The parent is the ListenerSet and not the Gateway, so the route binds to one
// port rather than to everything the shard serves.
func TestBuildTCPRoute_ParentsOnTheListenerSetSection(t *testing.T) {
	obj := builtRoute(t)

	parents, found, err := unstructured.NestedSlice(obj.Object, "spec", "parentRefs")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, parents, 1)

	p := parents[0].(map[string]any)
	assert.Equal(t, "ListenerSet", p["kind"])
	assert.Equal(t, "networkconfig", p["name"])
	assert.Equal(t, "direct", p["sectionName"])
	assert.Equal(t, ListenerSetGVK.Group, p["group"])
}

func TestBuildTCPRoute_BackendRefUsesAnInt64Port(t *testing.T) {
	obj := builtRoute(t)

	rules, found, err := unstructured.NestedSlice(obj.Object, "spec", "rules")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, rules, 1)

	refs, ok := rules[0].(map[string]any)["backendRefs"].([]any)
	require.True(t, ok)
	require.Len(t, refs, 1)

	b := refs[0].(map[string]any)
	assert.Equal(t, "redis", b["name"])
	assert.Equal(t, int64(6379), b["port"], "unstructured only accepts int64 for numbers")
}

func TestBuildTCPRoute_IsOwnedByTheSeal(t *testing.T) {
	obj := builtRoute(t)

	refs := obj.GetOwnerReferences()

	require.Len(t, refs, 1)
	assert.Equal(t, "Seal", refs[0].Kind)
	assert.True(t, *refs[0].Controller)
	assert.True(t, *refs[0].BlockOwnerDeletion, "the route must go when the Seal goes")
}

func TestBuildTCPRoute_EmptyServiceIsAnError(t *testing.T) {
	obj, err := BuildTCPRoute("networkconfig", "redis-7f3", "direct",
		Backend{Port: 6379}, sealOwner())

	require.Error(t, err)
	assert.Nil(t, obj)
	assert.Contains(t, err.Error(), "direct", "the message must name the broken exposure")
}

func TestBuildTCPRoute_PortOutsideTheRangeIsAnError(t *testing.T) {
	for _, port := range []int32{0, -1, 70000} {
		obj, err := BuildTCPRoute("networkconfig", "redis-7f3", "direct",
			Backend{Service: "redis", Port: port}, sealOwner())

		require.Error(t, err, "port %d", port)
		assert.Nil(t, obj, "port %d", port)
	}
}

func TestBuildTCPRoute_SurvivesDeepCopy(t *testing.T) {
	obj := builtRoute(t)

	assert.NotPanics(t, func() { _ = obj.DeepCopy() })
}

func TestRouteName_JoinsSealAndExposure(t *testing.T) {
	assert.Equal(t, "networkconfig-direct", routeName("networkconfig", "direct"))
}

func TestRouteName_StaysUnderTheObjectNameLimit(t *testing.T) {
	got := routeName(strings.Repeat("a", 200), strings.Repeat("b", 60))

	assert.NotEmpty(t, got)
	assert.LessOrEqual(t, len(got), maxNameLength,
		"a Kubernetes object name cannot exceed 253 characters")
}

// The route is looked up by this name on every reconcile, so an unstable name
// orphans the old object and writes a new one each time.
func TestRouteName_IsStableAcrossCalls(t *testing.T) {
	long := strings.Repeat("a", 200)

	assert.Equal(t, routeName(long, "direct"), routeName(long, "direct"))
}

// Truncation must not collapse two exposures onto one name, or one route
// silently replaces the other.
func TestRouteName_TruncatedNamesStayDistinct(t *testing.T) {
	long := strings.Repeat("a", 200)
	one := routeName(long, strings.Repeat("b", 60))
	two := routeName(long, strings.Repeat("b", 59)+"c")

	assert.NotEqual(t, one, two)
}

func TestRouteName_IsAValidDNSSubdomain(t *testing.T) {
	got := routeName(strings.Repeat("a", 200), strings.Repeat("b", 60))

	require.NotEmpty(t, got)
	assert.NotContains(t, got, "--", "a doubled separator reads as a broken name")
	assert.Regexp(t, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, got)
}
