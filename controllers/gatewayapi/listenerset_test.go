package gatewayapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
