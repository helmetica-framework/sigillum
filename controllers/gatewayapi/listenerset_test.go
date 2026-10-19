package gatewayapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/utils/ptr"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwac "sigs.k8s.io/gateway-api/applyconfiguration/apis/v1"
)

// listenerSet is a ListenerSet in redis-7f3 with spec, so a test can put in
// values a builder would never produce.
func listenerSet(spec gwv1.ListenerSetSpec) *gwv1.ListenerSet {
	return &gwv1.ListenerSet{
		ObjectMeta: metav1.ObjectMeta{Name: "networkconfig", Namespace: "redis-7f3"},
		Spec:       spec,
	}
}

func parentRef(name, namespace string) gwv1.ParentGatewayReference {
	return gwv1.ParentGatewayReference{Name: gwv1.ObjectName(name), Namespace: ptr.To(gwv1.Namespace(namespace))}
}

func entry(name string, port int32) gwv1.ListenerEntry {
	return gwv1.ListenerEntry{Name: gwv1.SectionName(name), Protocol: gwv1.TCPProtocolType, Port: gwv1.PortNumber(port)}
}

func TestParentShard_ReadsNameAndNamespace(t *testing.T) {
	got, ok := ParentShard(listenerSet(gwv1.ListenerSetSpec{ParentRef: parentRef("tcp-0", "kgateway-system")}))

	require.True(t, ok)
	assert.Equal(t, ShardRef{Name: "tcp-0", Namespace: "kgateway-system"}, got)
}

func TestParentShard_NoParentRef(t *testing.T) {
	_, ok := ParentShard(listenerSet(gwv1.ListenerSetSpec{}))

	assert.False(t, ok, "a parentless ListenerSet must not count against a shard")
}

func TestParentShard_NilObject(t *testing.T) {
	assert.NotPanics(t, func() {
		_, ok := ParentShard(nil)
		assert.False(t, ok)
	})
}

// Gateway API reads an omitted parentRef namespace as the object's own
// namespace. An empty string is not a namespace any real Gateway has, so a
// shard key carrying one matches nothing and its listeners go uncounted.
func TestParentShard_NoNamespaceMeansTheObjectsOwn(t *testing.T) {
	got, ok := ParentShard(listenerSet(gwv1.ListenerSetSpec{ParentRef: gwv1.ParentGatewayReference{Name: "tcp-0"}}))

	require.True(t, ok)
	assert.Equal(t, ShardRef{Name: "tcp-0", Namespace: "redis-7f3"}, got)
}

func TestParentShard_EmptyNamespaceMeansTheObjectsOwn(t *testing.T) {
	got, ok := ParentShard(listenerSet(gwv1.ListenerSetSpec{ParentRef: parentRef("tcp-0", "")}))

	require.True(t, ok)
	assert.Equal(t, ShardRef{Name: "tcp-0", Namespace: "redis-7f3"}, got)
}

func TestListeners_ReadsEveryEntryInOrder(t *testing.T) {
	ls := listenerSet(gwv1.ListenerSetSpec{Listeners: []gwv1.ListenerEntry{entry("zeta", 30009), entry("alpha", 30001)}})

	assert.Equal(t, []Listener{
		{Name: "zeta", Protocol: "TCP", Port: 30009},
		{Name: "alpha", Protocol: "TCP", Port: 30001},
	}, Listeners(ls), "order must follow the object, not sort")
}

func TestListeners_NoListeners(t *testing.T) {
	assert.Empty(t, Listeners(listenerSet(gwv1.ListenerSetSpec{})))
}

func TestListeners_NilObject(t *testing.T) {
	assert.NotPanics(t, func() { assert.Empty(t, Listeners(nil)) })
}

func TestListeners_SkipsAnOutOfRangePort(t *testing.T) {
	ls := listenerSet(gwv1.ListenerSetSpec{Listeners: []gwv1.ListenerEntry{
		entry("too-big", 70000), entry("zero", 0), entry("good", 30007),
	}})

	got := Listeners(ls)

	require.Len(t, got, 1, "a port outside 1..65535 cannot be real")
	assert.Equal(t, "good", got[0].Name)
}

// sealOwner is the controller owner reference a Seal produces.
func sealOwner() *metav1ac.OwnerReferenceApplyConfiguration {
	return metav1ac.OwnerReference().
		WithAPIVersion("seals.helmetica.io/v1").
		WithKind("Seal").
		WithName("networkconfig").
		WithUID("11111111-2222-3333-4444-555555555555").
		WithController(true).
		WithBlockOwnerDeletion(true)
}

func buildSet(parent ShardRef, listeners ...Listener) (*gwac.ListenerSetApplyConfiguration, error) {
	return BuildListenerSet("networkconfig", "redis-7f3", parent, listeners, sealOwner())
}

// builtSet is buildSet on tcp-0, for tests that expect no error.
func builtSet(t *testing.T, listeners ...Listener) *gwac.ListenerSetApplyConfiguration {
	t.Helper()

	ac, err := buildSet(ShardRef{Name: "tcp-0", Namespace: "kgateway-system"}, listeners...)
	require.NoError(t, err)

	return ac
}

// readBack turns a built set into the object the API server would store, so
// the readers can check what the builder wrote.
func readBack(ac *gwac.ListenerSetApplyConfiguration) *gwv1.ListenerSet {
	ls := &gwv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Name: *ac.Name, Namespace: *ac.Namespace}}

	p := ac.Spec.ParentRef
	ls.Spec.ParentRef = gwv1.ParentGatewayReference{Name: *p.Name, Namespace: p.Namespace}

	for _, l := range ac.Spec.Listeners {
		ls.Spec.Listeners = append(ls.Spec.Listeners, gwv1.ListenerEntry{
			Name: *l.Name, Protocol: *l.Protocol, Port: gwv1.PortNumber(*l.Port),
		})
	}

	return ls
}

func TestBuildListenerSet_ShapeAndNames(t *testing.T) {
	ac := builtSet(t, Listener{Name: "direct", Protocol: "TCP", Port: 30007})

	require.NotNil(t, ac)
	assert.Equal(t, "gateway.networking.k8s.io/v1", *ac.APIVersion)
	assert.Equal(t, "ListenerSet", *ac.Kind)
	assert.Equal(t, "networkconfig", *ac.Name)
	assert.Equal(t, "redis-7f3", *ac.Namespace)
}

func TestBuildListenerSet_ParentRefPointsAtTheShardGateway(t *testing.T) {
	ac := builtSet(t, Listener{Name: "direct", Protocol: "TCP", Port: 30007})

	p := ac.Spec.ParentRef
	assert.EqualValues(t, "tcp-0", *p.Name)
	assert.EqualValues(t, "kgateway-system", *p.Namespace)
	assert.EqualValues(t, "Gateway", *p.Kind)
	assert.EqualValues(t, "gateway.networking.k8s.io", *p.Group)
}

func TestBuildListenerSet_EveryListenerAllowsItsOwnNamespaceOnly(t *testing.T) {
	ac := builtSet(t,
		Listener{Name: "direct", Protocol: "TCP", Port: 30007},
		Listener{Name: "admin", Protocol: "TCP", Port: 30008},
	)

	require.Len(t, ac.Spec.Listeners, 2)
	for _, l := range ac.Spec.Listeners {
		assert.Equal(t, gwv1.NamespacesFromSame, *l.AllowedRoutes.Namespaces.From,
			"a route from another namespace must not be able to claim this port")
	}
}

func TestBuildListenerSet_SetsTheControllerOwnerReference(t *testing.T) {
	ac := builtSet(t, Listener{Name: "direct", Protocol: "TCP", Port: 30007})

	require.Len(t, ac.OwnerReferences, 1)
	ref := ac.OwnerReferences[0]
	assert.Equal(t, "Seal", *ref.Kind)
	assert.Equal(t, "networkconfig", *ref.Name)
	assert.EqualValues(t, "11111111-2222-3333-4444-555555555555", *ref.UID)
	assert.True(t, *ref.Controller)
	assert.True(t, *ref.BlockOwnerDeletion, "the ListenerSet must go when the Seal goes")
}

// The point of both guards: nothing the builder writes is unreadable, or
// allocation miscounts.
func TestBuildListenerSet_EverythingItWritesIsReadable(t *testing.T) {
	in := []Listener{
		{Name: "zeta", Protocol: "TCP", Port: 65535},
		{Name: "alpha", Protocol: "TCP", Port: 1},
	}
	parent := ShardRef{Name: "tcp-0", Namespace: "kgateway-system"}

	ac, err := buildSet(parent, in...)
	require.NoError(t, err)

	got, ok := ParentShard(readBack(ac))
	require.True(t, ok)
	assert.Equal(t, parent, got)
	assert.Equal(t, in, Listeners(readBack(ac)), "order must follow the caller, not sort")
}

// The delete signal stays distinct: no listeners is not a failure.
func TestBuildListenerSet_NoListenersIsNilWithNoError(t *testing.T) {
	ac, err := buildSet(ShardRef{Name: "tcp-0", Namespace: "kgateway-system"})

	require.NoError(t, err)
	assert.Nil(t, ac, "an empty ListenerSet is invalid, so the caller must delete instead of apply")
}

// A nameless parent reads back as no parent at all, so the set's ports would be
// invisible to allocation and handed to a second Seal.
func TestBuildListenerSet_ParentWithNoNameIsAnError(t *testing.T) {
	ac, err := buildSet(ShardRef{Namespace: "kgateway-system"},
		Listener{Name: "direct", Protocol: "TCP", Port: 30007})

	require.Error(t, err)
	assert.Nil(t, ac, "an error must not look like the delete signal")
}

// Listeners drops a port outside 1..65535, so writing one would hide a claimed
// port from allocation.
func TestBuildListenerSet_PortOutsideTheRangeIsAnError(t *testing.T) {
	for _, port := range []int32{0, -1, 70000} {
		ac, err := buildSet(ShardRef{Name: "tcp-0", Namespace: "kgateway-system"},
			Listener{Name: "bad", Protocol: "TCP", Port: port})

		require.Error(t, err, "port %d", port)
		assert.Nil(t, ac, "port %d", port)
	}
}

func TestBuildListenerSet_ErrorNamesThePortAndTheListener(t *testing.T) {
	_, err := buildSet(ShardRef{Name: "tcp-0", Namespace: "kgateway-system"},
		Listener{Name: "direct", Protocol: "TCP", Port: 30007},
		Listener{Name: "admin", Protocol: "TCP", Port: 99999})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "admin", "the listener must be named")
	assert.Contains(t, err.Error(), "99999", "the bad value must be shown")
}

// withStatus is a ListenerSet at generation, carrying conds per listener name.
func withStatus(generation int64, conds map[string][]metav1.Condition) *gwv1.ListenerSet {
	ls := listenerSet(gwv1.ListenerSetSpec{})
	ls.Generation = generation

	for name, cs := range conds {
		ls.Status.Listeners = append(ls.Status.Listeners, gwv1.ListenerEntryStatus{
			Name: gwv1.SectionName(name), Conditions: cs,
		})
	}

	return ls
}

func TestListenerStates_ReportsAConflictedListener(t *testing.T) {
	ls := withStatus(2, map[string][]metav1.Condition{
		"direct": {condition("Conflicted", metav1.ConditionTrue, 2), condition("Accepted", metav1.ConditionFalse, 2)},
	})

	assert.Equal(t, ListenerConflicted, ListenerStates(ls)["direct"])
}

func TestListenerStates_ConflictedFalseIsOK(t *testing.T) {
	ls := withStatus(2, map[string][]metav1.Condition{
		"direct": {condition("Conflicted", metav1.ConditionFalse, 2), condition("Accepted", metav1.ConditionTrue, 2)},
	})

	assert.Equal(t, ListenerOK, ListenerStates(ls)["direct"])
}

// Some gateway controllers leave Conflicted out when there is no conflict.
func TestListenerStates_AcceptedWithoutConflictedIsOK(t *testing.T) {
	ls := withStatus(2, map[string][]metav1.Condition{
		"direct": {condition("Accepted", metav1.ConditionTrue, 2)},
	})

	assert.Equal(t, ListenerOK, ListenerStates(ls)["direct"])
}

// The case that would loop: sigillum moved the listener, which bumped the
// generation, and the old conflict is still in the status.
func TestListenerStates_StaleConflictIsPending(t *testing.T) {
	ls := withStatus(3, map[string][]metav1.Condition{
		"direct": {condition("Conflicted", metav1.ConditionTrue, 2)},
	})

	assert.Equal(t, ListenerPending, ListenerStates(ls)["direct"],
		"a conflict on the old port must not move the new one")
}

func TestListenerStates_NoConditionsIsPending(t *testing.T) {
	ls := withStatus(2, map[string][]metav1.Condition{"direct": nil})

	assert.Equal(t, ListenerPending, ListenerStates(ls)["direct"])
}

func TestListenerStates_MissingListenerReadsAsPending(t *testing.T) {
	ls := withStatus(2, map[string][]metav1.Condition{
		"direct": {condition("Accepted", metav1.ConditionTrue, 2)},
	})

	assert.Equal(t, ListenerPending, ListenerStates(ls)["admin"],
		"the zero value must mean pending, so a missing entry is safe")
}

func TestListenerStates_ReadsEveryListener(t *testing.T) {
	ls := withStatus(2, map[string][]metav1.Condition{
		"direct": {condition("Conflicted", metav1.ConditionTrue, 2)},
		"admin":  {condition("Accepted", metav1.ConditionTrue, 2)},
	})

	assert.Equal(t, map[string]ListenerState{
		"direct": ListenerConflicted,
		"admin":  ListenerOK,
	}, ListenerStates(ls))
}

func TestListenerStates_SkipsANamelessEntry(t *testing.T) {
	ls := withStatus(2, map[string][]metav1.Condition{
		"":       {condition("Conflicted", metav1.ConditionTrue, 2)},
		"direct": {condition("Conflicted", metav1.ConditionTrue, 2)},
	})

	assert.Equal(t, map[string]ListenerState{"direct": ListenerConflicted}, ListenerStates(ls))
}

func TestListenerStates_NoStatusIsEmpty(t *testing.T) {
	assert.Empty(t, ListenerStates(listenerSet(gwv1.ListenerSetSpec{})))
}

func TestListenerStates_NilObjectIsEmpty(t *testing.T) {
	assert.Empty(t, ListenerStates(nil))
}

func TestListenerSetName_JoinsSealAndExposure(t *testing.T) {
	assert.Equal(t, "networkconfig-direct", ListenerSetName("networkconfig", "direct"))
}

// The route finds its listener by this name, so the two must never drift.
func TestListenerSetName_MatchesTheRouteName(t *testing.T) {
	long := strings.Repeat("a", 200)

	assert.Equal(t, RouteName("networkconfig", "direct"), ListenerSetName("networkconfig", "direct"))
	assert.Equal(t, RouteName(long, long), ListenerSetName(long, long))
}

func TestListenerSetName_LongNamesAreValid(t *testing.T) {
	name := ListenerSetName(strings.Repeat("a", 200), strings.Repeat("b", 200))

	assert.LessOrEqual(t, len(name), maxNameLength)
	assert.Empty(t, validation.IsDNS1123Subdomain(name))
}
