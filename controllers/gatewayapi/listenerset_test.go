package gatewayapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
)

// rawListenerSet builds a ListenerSet from raw maps, so a test can put values
// in that a builder would never produce.
func rawListenerSet(t *testing.T, spec map[string]any) *unstructured.Unstructured {
	t.Helper()

	obj := New(ListenerSetGVK)
	obj.SetName("networkconfig")
	obj.SetNamespace("redis-7f3")
	require.NoError(t, unstructured.SetNestedMap(obj.Object, spec, "spec"))

	return obj
}

func parentRef(name, namespace string) map[string]any {
	return map[string]any{
		"group":     ListenerSetGVK.Group,
		"kind":      "Gateway",
		"name":      name,
		"namespace": namespace,
	}
}

func listener(name string, port int64) map[string]any {
	return map[string]any{"name": name, "protocol": "TCP", "port": port}
}

func TestParentShard_ReadsNameAndNamespace(t *testing.T) {
	obj := rawListenerSet(t, map[string]any{
		"parentRef": parentRef("tcp-0", "kgateway-system"),
	})

	got, ok := ParentShard(obj)

	require.True(t, ok)
	assert.Equal(t, ShardRef{Name: "tcp-0", Namespace: "kgateway-system"}, got)
}

func TestParentShard_NoParentRef(t *testing.T) {
	obj := rawListenerSet(t, map[string]any{})

	_, ok := ParentShard(obj)

	assert.False(t, ok, "a parentless ListenerSet must not count against a shard")
}

func TestParentShard_EmptyName(t *testing.T) {
	obj := rawListenerSet(t, map[string]any{
		"parentRef": parentRef("", "kgateway-system"),
	})

	_, ok := ParentShard(obj)

	assert.False(t, ok)
}

func TestParentShard_NilObject(t *testing.T) {
	assert.NotPanics(t, func() {
		_, ok := ParentShard(nil)
		assert.False(t, ok)
	})
}

func TestListeners_ReadsEveryEntry(t *testing.T) {
	obj := rawListenerSet(t, map[string]any{
		"parentRef": parentRef("tcp-0", "kgateway-system"),
		"listeners": []any{listener("direct", 30007), listener("admin", 30008)},
	})

	got := Listeners(obj)

	assert.Equal(t, []Listener{
		{Name: "direct", Protocol: "TCP", Port: 30007},
		{Name: "admin", Protocol: "TCP", Port: 30008},
	}, got)
}

func TestListeners_KeepsSpecOrder(t *testing.T) {
	obj := rawListenerSet(t, map[string]any{
		"listeners": []any{listener("zeta", 30009), listener("alpha", 30001)},
	})

	got := Listeners(obj)

	require.Len(t, got, 2)
	assert.Equal(t, "zeta", got[0].Name, "order must follow the object, not sort")
}

func TestListeners_NoListenersKey(t *testing.T) {
	obj := rawListenerSet(t, map[string]any{
		"parentRef": parentRef("tcp-0", "kgateway-system"),
	})

	assert.Empty(t, Listeners(obj))
}

func TestListeners_NoSpecAtAll(t *testing.T) {
	obj := New(ListenerSetGVK)

	assert.NotPanics(t, func() { assert.Empty(t, Listeners(obj)) })
}

func TestListeners_NilObject(t *testing.T) {
	assert.NotPanics(t, func() { assert.Empty(t, Listeners(nil)) })
}

// One object written by hand must not stop every other Seal reconciling, so a
// bad entry is skipped rather than fatal.
func TestListeners_SkipsAnEntryWithAStringPort(t *testing.T) {
	obj := rawListenerSet(t, map[string]any{
		"listeners": []any{
			map[string]any{"name": "bad", "protocol": "TCP", "port": "not-a-number"},
			listener("good", 30007),
		},
	})

	got := Listeners(obj)

	require.Len(t, got, 1)
	assert.Equal(t, "good", got[0].Name)
}

func TestListeners_SkipsAnEntryWithNoPort(t *testing.T) {
	obj := rawListenerSet(t, map[string]any{
		"listeners": []any{
			map[string]any{"name": "bad", "protocol": "TCP"},
			listener("good", 30007),
		},
	})

	require.Len(t, Listeners(obj), 1)
}

func TestListeners_SkipsAnOutOfRangePort(t *testing.T) {
	obj := rawListenerSet(t, map[string]any{
		"listeners": []any{
			listener("too-big", 70000),
			listener("zero", 0),
			listener("good", 30007),
		},
	})

	got := Listeners(obj)

	require.Len(t, got, 1, "a port outside 1..65535 cannot be real and would truncate on int32 conversion")
	assert.Equal(t, "good", got[0].Name)
}

func TestListeners_SkipsANonMapEntry(t *testing.T) {
	obj := rawListenerSet(t, map[string]any{
		"listeners": []any{"garbage", listener("good", 30007)},
	})

	require.Len(t, Listeners(obj), 1)
}

func TestListeners_KeepsAnEntryWithNoProtocol(t *testing.T) {
	obj := rawListenerSet(t, map[string]any{
		"listeners": []any{map[string]any{"name": "direct", "port": int64(30007)}},
	})

	got := Listeners(obj)

	require.Len(t, got, 1, "the port is what matters for allocation, so a missing protocol is not fatal")
	assert.EqualValues(t, 30007, got[0].Port)
	assert.Empty(t, got[0].Protocol)
}

// Gateway API reads an omitted parentRef namespace as the object's own
// namespace. An empty string is not a namespace any real Gateway has, so a
// shard key carrying one matches nothing and its listeners go uncounted.
func TestParentShard_NoNamespaceKeyMeansTheObjectsOwn(t *testing.T) {
	obj := rawListenerSet(t, map[string]any{
		"parentRef": map[string]any{
			"group": ListenerSetGVK.Group,
			"kind":  "Gateway",
			"name":  "tcp-0",
		},
	})

	got, ok := ParentShard(obj)

	require.True(t, ok)
	assert.Equal(t, ShardRef{Name: "tcp-0", Namespace: "redis-7f3"}, got)
}

func TestParentShard_EmptyNamespaceMeansTheObjectsOwn(t *testing.T) {
	obj := rawListenerSet(t, map[string]any{
		"parentRef": parentRef("tcp-0", ""),
	})

	got, ok := ParentShard(obj)

	require.True(t, ok)
	assert.Equal(t, ShardRef{Name: "tcp-0", Namespace: "redis-7f3"}, got)
}

// sealOwner is the controller owner reference a Seal produces.
func sealOwner() metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         "seals.helmetica.io/v1",
		Kind:               "Seal",
		Name:               "networkconfig",
		UID:                "11111111-2222-3333-4444-555555555555",
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}
}

// builtSet is BuildListenerSet over the common case, so a test can assert one
// thing without repeating five arguments.
func builtSet(t *testing.T, listeners ...Listener) *unstructured.Unstructured {
	t.Helper()

	obj, err := BuildListenerSet(
		"networkconfig", "redis-7f3",
		ShardRef{Name: "tcp-0", Namespace: "kgateway-system"},
		listeners, sealOwner(),
	)
	require.NoError(t, err)

	return obj
}

func TestBuildListenerSet_ShapeAndNames(t *testing.T) {
	obj := builtSet(t, Listener{Name: "direct", Protocol: "TCP", Port: 30007})

	require.NotNil(t, obj)
	assert.Equal(t, ListenerSetGVK, obj.GroupVersionKind())
	assert.Equal(t, "networkconfig", obj.GetName())
	assert.Equal(t, "redis-7f3", obj.GetNamespace())
}

func TestBuildListenerSet_ParentRefPointsAtTheShardGateway(t *testing.T) {
	obj := builtSet(t, Listener{Name: "direct", Protocol: "TCP", Port: 30007})
	require.NotNil(t, obj)

	parent, found, err := unstructured.NestedMap(obj.Object, "spec", "parentRef")

	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "tcp-0", parent["name"])
	assert.Equal(t, "kgateway-system", parent["namespace"])
	assert.Equal(t, "Gateway", parent["kind"])
	assert.Equal(t, ListenerSetGVK.Group, parent["group"])
}

func TestBuildListenerSet_WritesPortsAsInt64(t *testing.T) {
	obj := builtSet(t, Listener{Name: "direct", Protocol: "TCP", Port: 30007})
	require.NotNil(t, obj)

	listeners, found, err := unstructured.NestedSlice(obj.Object, "spec", "listeners")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, listeners, 1)

	l := listeners[0].(map[string]any)
	assert.Equal(t, "direct", l["name"])
	assert.Equal(t, "TCP", l["protocol"])
	assert.Equal(t, int64(30007), l["port"], "unstructured only accepts int64 for numbers")
}

func TestBuildListenerSet_EveryListenerAllowsItsOwnNamespaceOnly(t *testing.T) {
	obj := builtSet(t,
		Listener{Name: "direct", Protocol: "TCP", Port: 30007},
		Listener{Name: "admin", Protocol: "TCP", Port: 30008},
	)
	require.NotNil(t, obj)

	listeners, _, _ := unstructured.NestedSlice(obj.Object, "spec", "listeners")
	require.Len(t, listeners, 2)

	for _, raw := range listeners {
		from, found, err := unstructured.NestedString(raw.(map[string]any),
			"allowedRoutes", "namespaces", "from")

		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, "Same", from,
			"a route from another namespace must not be able to claim this port")
	}
}

func TestBuildListenerSet_SetsTheControllerOwnerReference(t *testing.T) {
	obj := builtSet(t, Listener{Name: "direct", Protocol: "TCP", Port: 30007})
	require.NotNil(t, obj)

	refs := obj.GetOwnerReferences()

	require.Len(t, refs, 1)
	assert.Equal(t, "Seal", refs[0].Kind)
	assert.Equal(t, "networkconfig", refs[0].Name)
	assert.EqualValues(t, "11111111-2222-3333-4444-555555555555", refs[0].UID)
	assert.True(t, *refs[0].Controller)
	assert.True(t, *refs[0].BlockOwnerDeletion,
		"the ListenerSet must go when the Seal goes")
}

func TestBuildListenerSet_NoListenersReturnsNil(t *testing.T) {
	assert.Nil(t, builtSet(t),
		"an empty ListenerSet is invalid, so the caller must delete instead of apply")
}

func TestBuildListenerSet_RoundTripsThroughListeners(t *testing.T) {
	in := []Listener{
		{Name: "direct", Protocol: "TCP", Port: 30007},
		{Name: "admin", Protocol: "TCP", Port: 30008},
	}

	assert.Equal(t, in, Listeners(builtSet(t, in...)),
		"the reader and the builder have to agree, or allocation miscounts")
}

func TestBuildListenerSet_KeepsListenerOrder(t *testing.T) {
	obj := builtSet(t,
		Listener{Name: "zeta", Protocol: "TCP", Port: 30009},
		Listener{Name: "alpha", Protocol: "TCP", Port: 30001},
	)

	got := Listeners(obj)

	require.Len(t, got, 2)
	assert.Equal(t, "zeta", got[0].Name, "order must follow the caller, not sort")
}

// DeepCopy walks every value and panics on a type unstructured cannot hold,
// which is what an int32 port would be.
func TestBuildListenerSet_SurvivesTheUnstructuredConverter(t *testing.T) {
	obj := builtSet(t, Listener{Name: "direct", Protocol: "TCP", Port: 30007})
	require.NotNil(t, obj)

	assert.NotPanics(t, func() { _ = obj.DeepCopy() })
}

// buildSet is builtSet without the NoError, for the tests that want the error.
func buildSet(parent ShardRef, listeners ...Listener) (*unstructured.Unstructured, error) {
	return BuildListenerSet("networkconfig", "redis-7f3", parent, listeners, sealOwner())
}

// A nameless parent reads back as no parent at all, so the set's ports would be
// invisible to allocation and handed to a second Seal.
func TestBuildListenerSet_ParentWithNoNameIsAnError(t *testing.T) {
	obj, err := buildSet(ShardRef{Namespace: "kgateway-system"},
		Listener{Name: "direct", Protocol: "TCP", Port: 30007})

	require.Error(t, err)
	assert.Nil(t, obj, "an error must not look like the delete signal")
}

// Listeners drops a port outside 1..65535, so writing one would hide a claimed
// port from allocation.
func TestBuildListenerSet_PortOutsideTheRangeIsAnError(t *testing.T) {
	for _, port := range []int32{0, -1, 70000} {
		obj, err := buildSet(ShardRef{Name: "tcp-0", Namespace: "kgateway-system"},
			Listener{Name: "bad", Protocol: "TCP", Port: port})

		require.Error(t, err, "port %d", port)
		assert.Nil(t, obj, "port %d", port)
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

// The delete signal stays distinct: no listeners is not a failure.
func TestBuildListenerSet_NoListenersIsNilWithNoError(t *testing.T) {
	obj, err := buildSet(ShardRef{Name: "tcp-0", Namespace: "kgateway-system"})

	require.NoError(t, err)
	assert.Nil(t, obj)
}

// The point of both guards: nothing the builder writes can be unreadable.
func TestBuildListenerSet_EverythingItWritesIsReadable(t *testing.T) {
	in := []Listener{
		{Name: "direct", Protocol: "TCP", Port: 1},
		{Name: "admin", Protocol: "TCP", Port: 65535},
	}
	parent := ShardRef{Name: "tcp-0", Namespace: "kgateway-system"}

	obj, err := buildSet(parent, in...)
	require.NoError(t, err)

	got, ok := ParentShard(obj)
	require.True(t, ok)
	assert.Equal(t, parent, got)
	assert.Equal(t, in, Listeners(obj))
}

// listenerSetWithStatus builds a ListenerSet at generation, carrying the given
// conditions per listener name.
func listenerSetWithStatus(t *testing.T, generation int64, conds map[string][]any) *unstructured.Unstructured {
	t.Helper()

	obj := New(ListenerSetGVK)
	obj.SetName("networkconfig")
	obj.SetNamespace("redis-7f3")
	obj.SetGeneration(generation)

	listeners := make([]any, 0, len(conds))
	for name, cs := range conds {
		listeners = append(listeners, map[string]any{"name": name, "conditions": cs})
	}
	require.NoError(t, unstructured.SetNestedSlice(obj.Object, listeners, "status", "listeners"))

	return obj
}

func TestListenerStates_ReportsAConflictedListener(t *testing.T) {
	obj := listenerSetWithStatus(t, 2, map[string][]any{
		"direct": {condition("Conflicted", "True", 2), condition("Accepted", "False", 2)},
	})

	assert.Equal(t, ListenerConflicted, ListenerStates(obj)["direct"])
}

func TestListenerStates_ConflictedFalseIsOK(t *testing.T) {
	obj := listenerSetWithStatus(t, 2, map[string][]any{
		"direct": {condition("Conflicted", "False", 2), condition("Accepted", "True", 2)},
	})

	assert.Equal(t, ListenerOK, ListenerStates(obj)["direct"])
}

// Some gateway controllers leave Conflicted out when there is no conflict.
func TestListenerStates_AcceptedWithoutConflictedIsOK(t *testing.T) {
	obj := listenerSetWithStatus(t, 2, map[string][]any{
		"direct": {condition("Accepted", "True", 2)},
	})

	assert.Equal(t, ListenerOK, ListenerStates(obj)["direct"])
}

// The case that would loop: sigillum moved the listener, which bumped the
// generation, and the old conflict is still in the status.
func TestListenerStates_StaleConflictIsPending(t *testing.T) {
	obj := listenerSetWithStatus(t, 3, map[string][]any{
		"direct": {condition("Conflicted", "True", 2)},
	})

	assert.Equal(t, ListenerPending, ListenerStates(obj)["direct"],
		"a conflict on the old port must not move the new one")
}

func TestListenerStates_NoConditionsIsPending(t *testing.T) {
	obj := listenerSetWithStatus(t, 2, map[string][]any{"direct": {}})

	assert.Equal(t, ListenerPending, ListenerStates(obj)["direct"])
}

func TestListenerStates_MissingListenerReadsAsPending(t *testing.T) {
	obj := listenerSetWithStatus(t, 2, map[string][]any{
		"direct": {condition("Accepted", "True", 2)},
	})

	assert.Equal(t, ListenerPending, ListenerStates(obj)["admin"],
		"the zero value must mean pending, so a missing entry is safe")
}

func TestListenerStates_ReadsEveryListener(t *testing.T) {
	obj := listenerSetWithStatus(t, 2, map[string][]any{
		"direct": {condition("Conflicted", "True", 2)},
		"admin":  {condition("Accepted", "True", 2)},
	})

	assert.Equal(t, map[string]ListenerState{
		"direct": ListenerConflicted,
		"admin":  ListenerOK,
	}, ListenerStates(obj))
}

func TestListenerStates_MalformedEntriesAreSkipped(t *testing.T) {
	obj := New(ListenerSetGVK)
	obj.SetGeneration(2)
	require.NoError(t, unstructured.SetNestedSlice(obj.Object, []any{
		"not a map",
		map[string]any{"conditions": []any{condition("Conflicted", "True", 2)}},
		map[string]any{"name": "direct", "conditions": []any{condition("Conflicted", "True", 2)}},
	}, "status", "listeners"))

	assert.Equal(t, map[string]ListenerState{"direct": ListenerConflicted}, ListenerStates(obj))
}

func TestListenerStates_NoStatusIsEmpty(t *testing.T) {
	assert.Empty(t, ListenerStates(New(ListenerSetGVK)))
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
