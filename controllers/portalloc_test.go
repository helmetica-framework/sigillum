package controllers

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	"github.com/helmetica-framework/sigillum/controllers/gatewayapi"
)

func shard(name string) gatewayapi.ShardRef {
	return gatewayapi.ShardRef{Name: name, Namespace: "kgateway-system"}
}

// listenerSet builds a ListenerSet parented to shardName, with one listener
// per port named after its index.
func listenerSet(t *testing.T, name, namespace, shardName string, ports ...int64) unstructured.Unstructured {
	t.Helper()

	listeners := make([]any, 0, len(ports))
	for i, p := range ports {
		listeners = append(listeners, map[string]any{
			"name":     string(rune('a' + i)),
			"protocol": "TCP",
			"port":     p,
		})
	}

	obj := gatewayapi.New(gatewayapi.ListenerSetGVK)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	require.NoError(t, unstructured.SetNestedMap(obj.Object, map[string]any{
		"parentRef": map[string]any{
			"group":     gatewayapi.GatewayGVK.Group,
			"kind":      "Gateway",
			"name":      shardName,
			"namespace": "kgateway-system",
		},
		"listeners": listeners,
	}, "spec"))

	return *obj
}

func portRange() sealsv1.PortRange { return sealsv1.PortRange{From: 30000, To: 30255} }

func seeded() *rand.Rand { return rand.New(rand.NewPCG(1, 42)) }

func TestTakenPorts_CollectsPortsOnTheShard(t *testing.T) {
	sets := []unstructured.Unstructured{
		listenerSet(t, "a", "ns-a", "tcp-0", 30000, 30001),
		listenerSet(t, "b", "ns-b", "tcp-0", 30002),
	}

	taken := takenPorts(sets, shard("tcp-0"), nil)

	assert.Len(t, taken, 3)
	for _, p := range []int32{30000, 30001, 30002} {
		assert.Contains(t, taken, p)
	}
}

// Each shard Gateway has its own IP, so the same port on two shards is not a
// clash. Treating ports as cluster-wide would waste most of the range.
func TestTakenPorts_IgnoresOtherShards(t *testing.T) {
	sets := []unstructured.Unstructured{
		listenerSet(t, "a", "ns-a", "tcp-0", 30000),
		listenerSet(t, "b", "ns-b", "tcp-1", 30001),
	}

	taken := takenPorts(sets, shard("tcp-0"), nil)

	assert.Contains(t, taken, int32(30000))
	assert.NotContains(t, taken, int32(30001))
}

func TestTakenPorts_SkipsOurOwnListenerSet(t *testing.T) {
	own := listenerSet(t, "networkconfig", "redis-7f3", "tcp-0", 30007)
	sets := []unstructured.Unstructured{own, listenerSet(t, "b", "ns-b", "tcp-0", 30008)}

	taken := takenPorts(sets, shard("tcp-0"), &own)

	assert.NotContains(t, taken, int32(30007), "our own port must stay allocatable to us")
	assert.Contains(t, taken, int32(30008))
}

// The caller's copy of own comes out of a List, so it is a different object
// from the entry in sets. Matching on pointer identity would never fire.
func TestTakenPorts_MatchesOwnByNameAndNamespace(t *testing.T) {
	sets := []unstructured.Unstructured{listenerSet(t, "networkconfig", "redis-7f3", "tcp-0", 30007)}
	separateCopy := listenerSet(t, "networkconfig", "redis-7f3", "tcp-0", 30007)

	taken := takenPorts(sets, shard("tcp-0"), &separateCopy)

	assert.NotContains(t, taken, int32(30007))
}

func TestTakenPorts_ASameNamedSetInAnotherNamespaceIsNotOurs(t *testing.T) {
	sets := []unstructured.Unstructured{listenerSet(t, "networkconfig", "other-ns", "tcp-0", 30007)}
	own := listenerSet(t, "networkconfig", "redis-7f3", "tcp-0")

	taken := takenPorts(sets, shard("tcp-0"), &own)

	assert.Contains(t, taken, int32(30007), "another namespace's set holds that port for real")
}

func TestTakenPorts_NoListenerSets(t *testing.T) {
	taken := takenPorts(nil, shard("tcp-0"), nil)

	assert.NotNil(t, taken, "callers add to this map, so it must never be nil")
	assert.Empty(t, taken)
}

func TestTakenPorts_ListenerSetWithoutListenersDoesNotPanic(t *testing.T) {
	obj := gatewayapi.New(gatewayapi.ListenerSetGVK)
	obj.SetName("empty")
	obj.SetNamespace("ns")

	assert.NotPanics(t, func() {
		takenPorts([]unstructured.Unstructured{*obj}, shard("tcp-0"), nil)
	})
}

func TestTakenPorts_ParentlessSetCountsAgainstNoShard(t *testing.T) {
	obj := gatewayapi.New(gatewayapi.ListenerSetGVK)
	obj.SetName("orphan")
	obj.SetNamespace("ns")
	require.NoError(t, unstructured.SetNestedSlice(obj.Object,
		[]any{map[string]any{"name": "x", "protocol": "TCP", "port": int64(30007)}},
		"spec", "listeners"))

	taken := takenPorts([]unstructured.Unstructured{*obj}, shard("tcp-0"), nil)

	assert.Empty(t, taken)
}

// Our own ports are normally skipped so we keep them. A conflicted listener is
// the exception: the gateway controller rejected that port, so handing it back
// would reproduce the conflict.
func TestTakenPortsWithConflicts_ConflictedPortStaysTaken(t *testing.T) {
	own := listenerSet(t, "networkconfig", "redis-7f3", "tcp-0", 30007, 30008)

	taken := takenPortsWithConflicts(
		[]unstructured.Unstructured{own}, shard("tcp-0"), &own,
		map[string]struct{}{"a": {}}, // listener "a" holds 30007
	)

	assert.Contains(t, taken, int32(30007), "a conflicted port must not be handed back to us")
	assert.NotContains(t, taken, int32(30008), "our healthy port stays ours")
}

func TestTakenPortsWithConflicts_NoConflictsMatchesTakenPorts(t *testing.T) {
	own := listenerSet(t, "networkconfig", "redis-7f3", "tcp-0", 30007)
	sets := []unstructured.Unstructured{own, listenerSet(t, "b", "ns-b", "tcp-0", 30008)}

	assert.Equal(t,
		takenPorts(sets, shard("tcp-0"), &own),
		takenPortsWithConflicts(sets, shard("tcp-0"), &own, nil))
}

func TestAllocatePort_PicksAFreePortInRange(t *testing.T) {
	taken := map[int32]struct{}{30000: {}, 30001: {}, 30002: {}}

	got, err := allocatePort(portRange(), taken, seeded())

	require.NoError(t, err)
	assert.GreaterOrEqual(t, got, int32(30000))
	assert.LessOrEqual(t, got, int32(30255))
	assert.NotContains(t, taken, got)
}

func TestAllocatePort_FullRangeIsAnError(t *testing.T) {
	taken := map[int32]struct{}{}
	for p := int32(30000); p <= 30255; p++ {
		taken[p] = struct{}{}
	}

	_, err := allocatePort(portRange(), taken, seeded())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "30000", "the message reaches the user as the seal status")
	assert.Contains(t, err.Error(), "30255")
}

func TestAllocatePort_SinglePortRange(t *testing.T) {
	got, err := allocatePort(sealsv1.PortRange{From: 30007, To: 30007}, nil, seeded())

	require.NoError(t, err)
	assert.Equal(t, int32(30007), got)
}

func TestAllocatePort_SinglePortRangeAlreadyTaken(t *testing.T) {
	_, err := allocatePort(sealsv1.PortRange{From: 30007, To: 30007},
		map[int32]struct{}{30007: {}}, seeded())

	require.Error(t, err)
}

func TestAllocatePort_NilTakenMeansEverythingIsFree(t *testing.T) {
	got, err := allocatePort(portRange(), nil, seeded())

	require.NoError(t, err)
	assert.GreaterOrEqual(t, got, int32(30000))
	assert.LessOrEqual(t, got, int32(30255))
}

func TestAllocatePort_IsDeterministicUnderASeededRand(t *testing.T) {
	first, err := allocatePort(portRange(), nil, rand.New(rand.NewPCG(1, 7)))
	require.NoError(t, err)

	second, err := allocatePort(portRange(), nil, rand.New(rand.NewPCG(1, 7)))
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

func TestAllocatePort_AvoidsTheOnlyTakenPort(t *testing.T) {
	// Only 30008 is free, so the answer is fixed whatever the random source does.
	taken := map[int32]struct{}{}
	for p := int32(30000); p <= 30255; p++ {
		taken[p] = struct{}{}
	}
	delete(taken, 30008)

	got, err := allocatePort(portRange(), taken, seeded())

	require.NoError(t, err)
	assert.Equal(t, int32(30008), got)
}

func TestAllocatePort_NeverRepeatsWhileTheTakenSetGrows(t *testing.T) {
	taken := map[int32]struct{}{}
	r := seeded()

	for i := range 256 {
		got, err := allocatePort(portRange(), taken, r)
		require.NoError(t, err, "iteration %d", i)
		require.NotContains(t, taken, got, "port %d handed out twice at iteration %d", got, i)
		taken[got] = struct{}{}
	}

	_, err := allocatePort(portRange(), taken, r)
	assert.Error(t, err, "the 257th allocation must fail on a 256-port range")
}

func TestAllocatePort_DoesNotAlwaysReturnTheLowestFreePort(t *testing.T) {
	// Lowest-first would hand a just-released port straight to the next
	// service while clients still hold connections to the old one.
	r := seeded()
	seen := map[int32]struct{}{}

	for range 20 {
		got, err := allocatePort(portRange(), nil, r)
		require.NoError(t, err)
		seen[got] = struct{}{}
	}

	assert.Greater(t, len(seen), 1, "20 allocations from an empty range must not all be the same port")
}

func TestListAllListenerSets_ReturnsEveryItem(t *testing.T) {
	a := listenerSet(t, "a", "ns-a", "tcp-0", 30000)
	b := listenerSet(t, "b", "ns-b", "tcp-1", 30001)
	m, _ := newManager(&a, &b)

	got, err := m.listAllListenerSets(context.Background(), true)

	require.NoError(t, err)
	assert.Len(t, got, 2, "allocation is cluster-wide, so the list must not be namespaced")
}

func TestListAllListenerSets_EmptyCluster(t *testing.T) {
	m, _ := newManager()

	got, err := m.listAllListenerSets(context.Background(), false)

	require.NoError(t, err)
	assert.Empty(t, got)
}

// Live must read the API server, not the cache. The two readers are given
// different contents here so a wrong pick is visible.
func TestListAllListenerSets_LivePicksTheAPIReader(t *testing.T) {
	scheme := newTestScheme()
	cached := listenerSet(t, "stale", "ns-a", "tcp-0", 30000)
	fresh := listenerSet(t, "fresh", "ns-b", "tcp-0", 30001)

	m := &SealManager{
		Client:    fake.NewClientBuilder().WithScheme(scheme).WithObjects(&cached).Build(),
		APIReader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(&fresh).Build(),
		Scheme:    scheme,
		Log:       logr.Discard(),
	}

	live, err := m.listAllListenerSets(context.Background(), true)
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, "fresh", live[0].GetName(), "a live read must bypass the cache")

	stale, err := m.listAllListenerSets(context.Background(), false)
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, "stale", stale[0].GetName(), "a cached read must not pay for an uncached list")
}
