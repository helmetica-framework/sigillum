package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	"github.com/helmetica-framework/sigillum/controllers/gatewayapi"
)

const shardPoolLabel = "sigillum.io/shard-pool"

// tcpClass is a TCP ExposureClass with a shard pool selector and a port range.
func tcpClass(maxPerShard int32) *sealsv1.ExposureClass {
	c := class("tcp", sealsv1.ProtocolTCP)
	c.Spec.ShardSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{shardPoolLabel: "tcp"},
	}
	c.Spec.PortRange = &sealsv1.PortRange{From: 30000, To: 30255}
	c.Spec.MaxListenersPerShard = maxPerShard

	return &c
}

func gatewayObj(name, namespace string, labels map[string]string) unstructured.Unstructured {
	obj := gatewayapi.New(gatewayapi.GatewayGVK)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	obj.SetLabels(labels)

	return *obj
}

func tcpShard(name string) unstructured.Unstructured {
	return gatewayObj(name, "kgateway-system", map[string]string{shardPoolLabel: "tcp"})
}

func TestEffectiveMax_ZeroMeansTheAPILimit(t *testing.T) {
	assert.EqualValues(t, apiListenerLimit, effectiveMax(0),
		"0 means the API limit, not unlimited: there is no unlimited here")
}

func TestEffectiveMax_PassesThroughASmallerValue(t *testing.T) {
	assert.EqualValues(t, 10, effectiveMax(10))
}

func TestEffectiveMax_ClampsAboveTheAPILimit(t *testing.T) {
	assert.EqualValues(t, apiListenerLimit, effectiveMax(200),
		"the CRD caps this at 64, but an older object could still hold more")
}

func TestSortShards_IsStableAcrossListOrder(t *testing.T) {
	a := []gatewayapi.ShardRef{tcpRef("tcp-2"), tcpRef("tcp-0"), tcpRef("tcp-1")}
	b := []gatewayapi.ShardRef{tcpRef("tcp-1"), tcpRef("tcp-2"), tcpRef("tcp-0")}

	sortShards(a)
	sortShards(b)

	assert.Equal(t, a, b, "an unstable order moves exposures between shards for no reason")
	assert.Equal(t, "tcp-0", a[0].Name)
}

func TestSortShards_OrdersByNamespaceThenName(t *testing.T) {
	in := []gatewayapi.ShardRef{
		{Namespace: "b", Name: "a"},
		{Namespace: "a", Name: "z"},
		{Namespace: "a", Name: "a"},
	}

	sortShards(in)

	assert.Equal(t, []gatewayapi.ShardRef{
		{Namespace: "a", Name: "a"},
		{Namespace: "a", Name: "z"},
		{Namespace: "b", Name: "a"},
	}, in)
}

func TestSortShards_EmptyAndSingle(t *testing.T) {
	assert.NotPanics(t, func() {
		sortShards(nil)
		sortShards([]gatewayapi.ShardRef{tcpRef("tcp-0")})
	})
}

func TestListShards_ReturnsMatchingGatewaysSorted(t *testing.T) {
	two, one := tcpShard("tcp-2"), tcpShard("tcp-1")
	m, _ := newManager(&two, &one)

	got, err := m.listShards(context.Background(), tcpClass(64))

	require.NoError(t, err)
	assert.Equal(t, []gatewayapi.ShardRef{tcpRef("tcp-1"), tcpRef("tcp-2")}, got)
}

func TestListShards_IgnoresGatewaysWithoutTheLabel(t *testing.T) {
	match := tcpShard("tcp-0")
	other := gatewayObj("web", "kgateway-system", map[string]string{shardPoolLabel: "http"})
	bare := gatewayObj("bare", "kgateway-system", nil)
	m, _ := newManager(&match, &other, &bare)

	got, err := m.listShards(context.Background(), tcpClass(64))

	require.NoError(t, err)
	assert.Equal(t, []gatewayapi.ShardRef{tcpRef("tcp-0")}, got)
}

func TestListShards_FindsShardsAcrossNamespaces(t *testing.T) {
	here := tcpShard("tcp-0")
	there := gatewayObj("tcp-0", "other-system", map[string]string{shardPoolLabel: "tcp"})
	m, _ := newManager(&here, &there)

	got, err := m.listShards(context.Background(), tcpClass(64))

	require.NoError(t, err)
	assert.Len(t, got, 2, "shards are cluster-wide, so the list must not be namespaced")
}

// metav1 spells a nil selector and an empty one differently: nil matches
// nothing, empty matches everything. A class with no selector quietly adopting
// every Gateway in the cluster would be a very bad surprise.
func TestListShards_NilSelectorMatchesNothing(t *testing.T) {
	gw := tcpShard("tcp-0")
	c := tcpClass(64)
	c.Spec.ShardSelector = nil
	m, _ := newManager(&gw)

	got, err := m.listShards(context.Background(), c)

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestListShards_EmptyCluster(t *testing.T) {
	m, _ := newManager()

	got, err := m.listShards(context.Background(), tcpClass(64))

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestShardCounts_CountsListenersPerShard(t *testing.T) {
	sets := []unstructured.Unstructured{
		listenerSet(t, "a", "ns-a", "tcp-0", 30000, 30001),
		listenerSet(t, "b", "ns-b", "tcp-1", 30002),
	}

	counts := shardCounts(sets)

	assert.Equal(t, 2, counts[tcpRef("tcp-0")])
	assert.Equal(t, 1, counts[tcpRef("tcp-1")])
}

func TestShardCounts_SumsSetsOnTheSameShard(t *testing.T) {
	sets := []unstructured.Unstructured{
		listenerSet(t, "a", "ns-a", "tcp-0", 30000),
		listenerSet(t, "b", "ns-b", "tcp-0", 30001, 30002),
	}

	assert.Equal(t, 3, shardCounts(sets)[tcpRef("tcp-0")])
}

func TestShardCounts_ParentlessSetCountsNowhere(t *testing.T) {
	obj := gatewayapi.New(gatewayapi.ListenerSetGVK)
	obj.SetName("orphan")
	obj.SetNamespace("ns")
	require.NoError(t, unstructured.SetNestedSlice(obj.Object,
		[]any{map[string]any{"name": "x", "protocol": "TCP", "port": int64(30007)}},
		"spec", "listeners"))

	assert.Empty(t, shardCounts([]unstructured.Unstructured{*obj}))
}

func TestShardCounts_EmptyInputIsAUsableMap(t *testing.T) {
	counts := shardCounts(nil)

	require.NotNil(t, counts, "callers index into this, so it must never be nil")
	assert.Zero(t, counts[tcpRef("tcp-0")])
}

func TestStickyShard_KeepsAShardThatStillMatches(t *testing.T) {
	own := listenerSet(t, "networkconfig", "redis-7f3", "tcp-1", 30007)

	got, ok := stickyShard(&own, []gatewayapi.ShardRef{tcpRef("tcp-0"), tcpRef("tcp-1")})

	require.True(t, ok)
	assert.Equal(t, tcpRef("tcp-1"), got, "a published address must not move just because tcp-0 has room")
}

func TestStickyShard_ShardNoLongerMatches(t *testing.T) {
	own := listenerSet(t, "networkconfig", "redis-7f3", "tcp-9", 30007)

	_, ok := stickyShard(&own, []gatewayapi.ShardRef{tcpRef("tcp-0")})

	assert.False(t, ok, "a shard that left the pool must be re-placed, not kept")
}

func TestStickyShard_NoOwnListenerSet(t *testing.T) {
	_, ok := stickyShard(nil, []gatewayapi.ShardRef{tcpRef("tcp-0")})

	assert.False(t, ok)
}

func TestStickyShard_OwnWithoutAParent(t *testing.T) {
	obj := gatewayapi.New(gatewayapi.ListenerSetGVK)
	obj.SetName("networkconfig")
	obj.SetNamespace("redis-7f3")

	_, ok := stickyShard(obj, []gatewayapi.ShardRef{tcpRef("tcp-0")})

	assert.False(t, ok)
}

func TestStickyShard_NoShardsAtAll(t *testing.T) {
	own := listenerSet(t, "networkconfig", "redis-7f3", "tcp-0", 30007)

	_, ok := stickyShard(&own, nil)

	assert.False(t, ok)
}

func TestPlaceExposure_FirstShardWithRoom(t *testing.T) {
	shards := []gatewayapi.ShardRef{tcpRef("tcp-0"), tcpRef("tcp-1")}
	counts := map[gatewayapi.ShardRef]int{tcpRef("tcp-0"): 10}

	got, err := placeExposure(shards, counts, tcpClass(64))

	require.NoError(t, err)
	assert.Equal(t, tcpRef("tcp-0"), got)
}

func TestPlaceExposure_SpillsToTheNextShardWhenFull(t *testing.T) {
	shards := []gatewayapi.ShardRef{tcpRef("tcp-0"), tcpRef("tcp-1")}
	counts := map[gatewayapi.ShardRef]int{tcpRef("tcp-0"): 64}

	got, err := placeExposure(shards, counts, tcpClass(64))

	require.NoError(t, err)
	assert.Equal(t, tcpRef("tcp-1"), got)
}

func TestPlaceExposure_CountAtMaxMinusOneStillFits(t *testing.T) {
	shards := []gatewayapi.ShardRef{tcpRef("tcp-0")}
	counts := map[gatewayapi.ShardRef]int{tcpRef("tcp-0"): 63}

	got, err := placeExposure(shards, counts, tcpClass(64))

	require.NoError(t, err)
	assert.Equal(t, tcpRef("tcp-0"), got)
}

func TestPlaceExposure_UnknownShardCountsAsEmpty(t *testing.T) {
	shards := []gatewayapi.ShardRef{tcpRef("tcp-0")}

	got, err := placeExposure(shards, nil, tcpClass(64))

	require.NoError(t, err)
	assert.Equal(t, tcpRef("tcp-0"), got, "a brand new shard has no entry in counts")
}

func TestPlaceExposure_ZeroMaxMeansTheAPILimit(t *testing.T) {
	shards := []gatewayapi.ShardRef{tcpRef("tcp-0")}
	counts := map[gatewayapi.ShardRef]int{tcpRef("tcp-0"): 63}

	got, err := placeExposure(shards, counts, tcpClass(0))

	require.NoError(t, err)
	assert.Equal(t, tcpRef("tcp-0"), got, "0 means 64, not 'no room'")
}

func TestPlaceExposure_ZeroMaxStillFillsUpAt64(t *testing.T) {
	shards := []gatewayapi.ShardRef{tcpRef("tcp-0")}
	counts := map[gatewayapi.ShardRef]int{tcpRef("tcp-0"): 64}

	_, err := placeExposure(shards, counts, tcpClass(0))

	assert.Error(t, err, "0 must not mean unlimited")
}

func TestPlaceExposure_NoShardsIsItsOwnError(t *testing.T) {
	_, err := placeExposure(nil, nil, tcpClass(64))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "tcp", "the class must be named")
	assert.Contains(t, err.Error(), shardPoolLabel,
		"the selector is what the operator has to put on a Gateway")
}

func TestPlaceExposure_EveryShardFullIsAnError(t *testing.T) {
	shards := []gatewayapi.ShardRef{tcpRef("tcp-0"), tcpRef("tcp-1")}
	counts := map[gatewayapi.ShardRef]int{tcpRef("tcp-0"): 64, tcpRef("tcp-1"): 64}

	_, err := placeExposure(shards, counts, tcpClass(64))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "tcp-0")
	assert.Contains(t, err.Error(), "tcp-1")
}

// Creating gateways is out of scope, so the Seal's owner cannot fix this. The
// message is the only place they find out who can.
func TestPlaceExposure_ExhaustionMessageNamesTheActionRequired(t *testing.T) {
	shards := []gatewayapi.ShardRef{tcpRef("tcp-0")}
	counts := map[gatewayapi.ShardRef]int{tcpRef("tcp-0"): 64}

	_, err := placeExposure(shards, counts, tcpClass(64))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "tcp", "the class must be named")
	assert.Contains(t, err.Error(), "64", "the count tells the operator how full is full")
	assert.Contains(t, err.Error(), "platform operator",
		"the seal's owner cannot create gateways, so the message must say who can")
	assert.Contains(t, err.Error(), shardPoolLabel,
		"naming the label turns the message into an instruction")
}

func tcpRef(name string) gatewayapi.ShardRef {
	return gatewayapi.ShardRef{Name: name, Namespace: "kgateway-system"}
}

func TestEffectiveMax_ClampsANegativeValue(t *testing.T) {
	assert.EqualValues(t, apiListenerLimit, effectiveMax(-5),
		"a negative limit makes every shard look full, so nothing is ever placed")
}

// A parentRef with no namespace means the ListenerSet's own. An empty namespace
// would be a shard key no real Gateway matches, so these listeners would count
// against nothing and the shard could overfill past the cap.
func TestShardCounts_ParentWithoutANamespaceCountsOnTheLocalShard(t *testing.T) {
	obj := gatewayapi.New(gatewayapi.ListenerSetGVK)
	obj.SetName("a")
	obj.SetNamespace("kgateway-system")
	require.NoError(t, unstructured.SetNestedMap(obj.Object, map[string]any{
		"parentRef": map[string]any{
			"group": gatewayapi.GatewayGVK.Group,
			"kind":  "Gateway",
			"name":  "tcp-0",
		},
		"listeners": []any{map[string]any{"name": "d", "protocol": "TCP", "port": int64(30000)}},
	}, "spec"))

	counts := shardCounts([]unstructured.Unstructured{*obj})

	assert.Equal(t, 1, counts[tcpRef("tcp-0")])
	assert.Zero(t, counts[gatewayapi.ShardRef{Name: "tcp-0"}],
		"a namespace-less shard key is invisible to the cap")
}
