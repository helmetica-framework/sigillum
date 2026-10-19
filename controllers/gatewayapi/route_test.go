package gatewayapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
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
	assert.Equal(t, "networkconfig-direct", p["name"],
		"the route parents on the exposure's own ListenerSet, not a Seal-wide one")
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
	assert.Equal(t, "networkconfig-direct", RouteName("networkconfig", "direct"))
}

func TestRouteName_StaysUnderTheObjectNameLimit(t *testing.T) {
	got := RouteName(strings.Repeat("a", 200), strings.Repeat("b", 60))

	assert.NotEmpty(t, got)
	assert.LessOrEqual(t, len(got), maxNameLength,
		"a Kubernetes object name cannot exceed 253 characters")
}

// The route is looked up by this name on every reconcile, so an unstable name
// orphans the old object and writes a new one each time.
func TestRouteName_IsStableAcrossCalls(t *testing.T) {
	long := strings.Repeat("a", 200)

	assert.Equal(t, RouteName(long, "direct"), RouteName(long, "direct"))
}

// Truncation must not collapse two exposures onto one name, or one route
// silently replaces the other.
func TestRouteName_TruncatedNamesStayDistinct(t *testing.T) {
	long := strings.Repeat("a", 200)
	one := RouteName(long, strings.Repeat("b", 60))
	two := RouteName(long, strings.Repeat("b", 59)+"c")

	assert.NotEqual(t, one, two)
}

func TestRouteName_IsAValidDNSSubdomain(t *testing.T) {
	got := RouteName(strings.Repeat("a", 200), strings.Repeat("b", 60))

	require.NotEmpty(t, got)
	assert.NotContains(t, got, "--", "a doubled separator reads as a broken name")
	assert.Regexp(t, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, got)
}

// conditionWithMessage is condition with the message the gateway controller
// writes next to it.
func conditionWithMessage(condType, status, message string, observedGeneration int64) map[string]any {
	c := condition(condType, status, observedGeneration)
	c["message"] = message

	return c
}

// routeWithConditions is builtRoute at generation, with one parent carrying
// conds.
func routeWithConditions(t *testing.T, generation int64, conds ...any) *unstructured.Unstructured {
	t.Helper()

	obj := builtRoute(t)
	obj.SetGeneration(generation)
	require.NoError(t, unstructured.SetNestedSlice(obj.Object, []any{
		map[string]any{
			"parentRef": map[string]any{
				"group":       ListenerSetGVK.Group,
				"kind":        ListenerSetGVK.Kind,
				"name":        "networkconfig",
				"sectionName": "direct",
			},
			"controllerName": "kgateway.dev/kgateway",
			"conditions":     conds,
		},
	}, "status", "parents"))

	return obj
}

func TestRouteIsReady_AcceptedAndResolved(t *testing.T) {
	obj := routeWithConditions(t, 2,
		conditionWithMessage("Accepted", "True", "accepted", 2),
		conditionWithMessage("ResolvedRefs", "True", "resolved", 2),
	)

	ready, _ := RouteIsReady(obj)

	assert.True(t, ready)
}

func TestRouteIsReady_UnresolvedBackendIsNotReady(t *testing.T) {
	obj := routeWithConditions(t, 2,
		conditionWithMessage("Accepted", "True", "accepted", 2),
		conditionWithMessage("ResolvedRefs", "False", "service redis not found", 2),
	)

	ready, msg := RouteIsReady(obj)

	assert.False(t, ready)
	assert.Equal(t, "service redis not found", msg, "the route's own message must reach the user")
}

func TestRouteIsReady_NotAcceptedCarriesItsMessage(t *testing.T) {
	obj := routeWithConditions(t, 2,
		conditionWithMessage("Accepted", "False", "no listener named direct", 2),
		conditionWithMessage("ResolvedRefs", "True", "resolved", 2),
	)

	ready, msg := RouteIsReady(obj)

	assert.False(t, ready)
	assert.Equal(t, "no listener named direct", msg)
}

func TestRouteIsReady_NoStatusYet(t *testing.T) {
	ready, msg := RouteIsReady(builtRoute(t))

	assert.False(t, ready, "no conditions means the gateway controller has not looked yet")
	assert.NotEmpty(t, msg, "an empty message tells the user nothing")
}

func TestRouteIsReady_StaleConditionsDoNotCount(t *testing.T) {
	obj := routeWithConditions(t, 3,
		conditionWithMessage("Accepted", "True", "accepted", 2),
		conditionWithMessage("ResolvedRefs", "True", "resolved", 2),
	)

	ready, _ := RouteIsReady(obj)

	assert.False(t, ready, "conditions for an older spec say nothing about this one")
}

func TestRouteIsReady_NilIsNotReady(t *testing.T) {
	ready, _ := RouteIsReady(nil)

	assert.False(t, ready)
}

func TestRouteName_TruncatedNameIsAValidSubdomain(t *testing.T) {
	for _, seal := range []string{strings.Repeat("a.", 150), "b" + strings.Repeat("a.", 150), strings.Repeat("a--", 100)} {
		got := RouteName(seal, "direct")
		assert.Empty(t, validation.IsDNS1123Subdomain(got), "invalid route name %q", got)
	}
}
