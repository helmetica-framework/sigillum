package gatewayapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwac "sigs.k8s.io/gateway-api/applyconfiguration/apis/v1"
)

func redisBackend() Backend {
	return Backend{Service: "redis", Port: 6379}
}

// builtRoute is BuildTCPRoute over the common case.
func builtRoute(t *testing.T) *gwac.TCPRouteApplyConfiguration {
	t.Helper()

	ac, err := BuildTCPRoute("networkconfig", "redis-7f3", "direct", redisBackend(), sealOwner())
	require.NoError(t, err)
	require.NotNil(t, ac)

	return ac
}

func TestBuildTCPRoute_ShapeAndNames(t *testing.T) {
	ac := builtRoute(t)

	assert.Equal(t, "gateway.networking.k8s.io/v1", *ac.APIVersion)
	assert.Equal(t, "TCPRoute", *ac.Kind)
	assert.Equal(t, "networkconfig-direct", *ac.Name)
	assert.Equal(t, "redis-7f3", *ac.Namespace)
}

// The parent is the ListenerSet and not the Gateway, so the route binds to one
// port rather than to everything the shard serves.
func TestBuildTCPRoute_ParentsOnTheListenerSetSection(t *testing.T) {
	ac := builtRoute(t)

	require.Len(t, ac.Spec.ParentRefs, 1)
	p := ac.Spec.ParentRefs[0]
	assert.EqualValues(t, "ListenerSet", *p.Kind)
	assert.EqualValues(t, "networkconfig-direct", *p.Name,
		"the route parents on the exposure's own ListenerSet, not a Seal-wide one")
	assert.EqualValues(t, "direct", *p.SectionName)
	assert.EqualValues(t, "gateway.networking.k8s.io", *p.Group)
}

func TestBuildTCPRoute_SendsTrafficToTheBackend(t *testing.T) {
	ac := builtRoute(t)

	require.Len(t, ac.Spec.Rules, 1)
	require.Len(t, ac.Spec.Rules[0].BackendRefs, 1)
	b := ac.Spec.Rules[0].BackendRefs[0]
	assert.EqualValues(t, "redis", *b.Name)
	assert.EqualValues(t, 6379, *b.Port)
}

func TestBuildTCPRoute_IsOwnedByTheSeal(t *testing.T) {
	ac := builtRoute(t)

	require.Len(t, ac.OwnerReferences, 1)
	ref := ac.OwnerReferences[0]
	assert.Equal(t, "Seal", *ref.Kind)
	assert.True(t, *ref.Controller)
	assert.True(t, *ref.BlockOwnerDeletion, "the route must go when the Seal goes")
}

func TestBuildTCPRoute_EmptyServiceIsAnError(t *testing.T) {
	ac, err := BuildTCPRoute("networkconfig", "redis-7f3", "direct", Backend{Port: 6379}, sealOwner())

	require.Error(t, err)
	assert.Nil(t, ac)
	assert.Contains(t, err.Error(), "direct", "the message must name the broken exposure")
}

func TestBuildTCPRoute_PortOutsideTheRangeIsAnError(t *testing.T) {
	for _, port := range []int32{0, -1, 70000} {
		ac, err := BuildTCPRoute("networkconfig", "redis-7f3", "direct",
			Backend{Service: "redis", Port: port}, sealOwner())

		require.Error(t, err, "port %d", port)
		assert.Nil(t, ac, "port %d", port)
	}
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
func conditionWithMessage(condType string, status metav1.ConditionStatus, message string, observedGeneration int64) metav1.Condition {
	c := condition(condType, status, observedGeneration)
	c.Message = message

	return c
}

// routeWithConditions is a TCPRoute at generation, with one parent carrying
// conds.
func routeWithConditions(generation int64, conds ...metav1.Condition) *gwv1.TCPRoute {
	route := &gwv1.TCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "networkconfig-direct", Generation: generation}}
	route.Status.Parents = []gwv1.RouteParentStatus{{
		ParentRef:      gwv1.ParentReference{Name: "networkconfig-direct"},
		ControllerName: "kgateway.dev/kgateway",
		Conditions:     conds,
	}}

	return route
}

func TestRouteIsReady_AcceptedAndResolved(t *testing.T) {
	obj := routeWithConditions(2,
		conditionWithMessage("Accepted", metav1.ConditionTrue, "accepted", 2),
		conditionWithMessage("ResolvedRefs", metav1.ConditionTrue, "resolved", 2),
	)

	ready, _ := RouteIsReady(obj)

	assert.True(t, ready)
}

func TestRouteIsReady_UnresolvedBackendIsNotReady(t *testing.T) {
	obj := routeWithConditions(2,
		conditionWithMessage("Accepted", metav1.ConditionTrue, "accepted", 2),
		conditionWithMessage("ResolvedRefs", metav1.ConditionFalse, "service redis not found", 2),
	)

	ready, msg := RouteIsReady(obj)

	assert.False(t, ready)
	assert.Equal(t, "service redis not found", msg, "the route's own message must reach the user")
}

func TestRouteIsReady_NotAcceptedCarriesItsMessage(t *testing.T) {
	obj := routeWithConditions(2,
		conditionWithMessage("Accepted", metav1.ConditionFalse, "no listener named direct", 2),
		conditionWithMessage("ResolvedRefs", metav1.ConditionTrue, "resolved", 2),
	)

	ready, msg := RouteIsReady(obj)

	assert.False(t, ready)
	assert.Equal(t, "no listener named direct", msg)
}

func TestRouteIsReady_NoStatusYet(t *testing.T) {
	ready, msg := RouteIsReady(&gwv1.TCPRoute{})

	assert.False(t, ready, "no conditions means the gateway controller has not looked yet")
	assert.NotEmpty(t, msg, "an empty message tells the user nothing")
}

func TestRouteIsReady_StaleConditionsDoNotCount(t *testing.T) {
	obj := routeWithConditions(3,
		conditionWithMessage("Accepted", metav1.ConditionTrue, "accepted", 2),
		conditionWithMessage("ResolvedRefs", metav1.ConditionTrue, "resolved", 2),
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
