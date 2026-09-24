package gatewayapi

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ShardRef identifies a shard Gateway. It is used as a map key, so every field
// has to stay comparable.
type ShardRef struct {
	Namespace string
	Name      string
}

// Listener is one port on a shard Gateway, claimed for one exposure.
type Listener struct {
	Name     string
	Protocol string
	Port     int32
}

// ParentShard reads spec.parentRef off a ListenerSet. The second result is
// false when the object carries no usable parent.
func ParentShard(obj *unstructured.Unstructured) (ShardRef, bool) {
	if obj == nil {
		return ShardRef{}, false
	}

	ref, found, err := unstructured.NestedMap(obj.Object, "spec", "parentRef")
	if !found || err != nil {
		return ShardRef{}, false
	}

	name, _, _ := unstructured.NestedString(ref, "name")
	namespace, _, _ := unstructured.NestedString(ref, "namespace")

	// A parentless set must not silently count against some shard.
	if name == "" {
		return ShardRef{}, false
	}

	return ShardRef{
		Name:      name,
		Namespace: namespace,
	}, true
}

// Listeners reads spec.listeners off a ListenerSet.
//
// A malformed entry is skipped rather than fatal. This list is the source of
// truth for port allocation, and one bad object written by hand must not stop
// every other Seal from being reconciled.
func Listeners(obj *unstructured.Unstructured) []Listener {
	if obj == nil {
		return nil
	}

	listeners, found, err := unstructured.NestedSlice(obj.Object, "spec", "listeners")
	if !found || err != nil {
		return nil
	}

	allocated := []Listener{}

	for _, raw := range listeners {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}

		port, found, err := unstructured.NestedInt64(entry, "port")
		if !found || err != nil || port < 1 || port > 65535 {
			continue
		}

		// A missing name or protocol is not fatal. The port is what
		// allocation reads.
		name, _, _ := unstructured.NestedString(entry, "name")
		protocol, _, _ := unstructured.NestedString(entry, "protocol")

		allocated = append(allocated, Listener{
			Name:     name,
			Protocol: protocol,
			Port:     int32(port),
		})
	}

	return allocated
}
