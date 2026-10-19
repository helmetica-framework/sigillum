package gatewayapi

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// maxPort is the highest port number there is.
//
// The builder refuses anything outside 1..maxPort and Listeners drops it, and
// those two have to stay in step: a listener the reader cannot see is a port
// allocation hands out a second time.
const maxPort = 65535

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

// ListenerSetName is the name of the ListenerSet that holds one exposure's
// listener. It is the same as the exposure's TCPRoute name.
func ListenerSetName(sealName, exposureName string) string {
	return RouteName(sealName, exposureName)
}

// ListenerState is what the gateway controller last said about one listener's
// port.
type ListenerState int

const (
	// ListenerPending means there is no verdict for the current spec yet. It
	// is the zero value, so a listener missing from the status reads as
	// pending.
	ListenerPending ListenerState = iota
	ListenerConflicted
	ListenerOK
)

// BuildListenerSet renders the one ListenerSet a Seal owns, parented to a shard
// Gateway.
//
// A nil object with no error means there is nothing to write: spec.listeners
// requires at least one entry, so a Seal with no TCP exposures has no valid
// ListenerSet and the caller deletes the object instead.
//
// An error means the caller passed something that cannot be written. That is a
// bug in the caller, not a cluster state to reconcile, so it is never a nil
// object: the caller must not read it as "delete".
//
// Taking []Listener rather than raw maps is deliberate: every number written
// into an unstructured object has to be int64, and an int32 that slips through
// panics later, on conversion, far from the code that wrote it.
func BuildListenerSet(
	name, namespace string,
	parent ShardRef,
	listeners []Listener,
	owner metav1.OwnerReference,
) (*unstructured.Unstructured, error) {
	if len(listeners) == 0 {
		return nil, nil
	}

	// A nameless parent reads back as no parent at all, so this set's ports
	// would count against no shard and be handed to a second Seal.
	if parent.Name == "" {
		return nil, fmt.Errorf("listenerset %s/%s: parent shard has no name", namespace, name)
	}

	entries := make([]any, 0, len(listeners))

	for _, l := range listeners {
		if l.Port < 1 || l.Port > maxPort {
			return nil, fmt.Errorf("listenerset %s/%s: listener %q port %d is outside 1-%d",
				namespace, name, l.Name, l.Port, maxPort)
		}

		entries = append(entries, map[string]any{
			"name":     l.Name,
			"protocol": l.Protocol,
			"port":     int64(l.Port),
			"allowedRoutes": map[string]any{
				"namespaces": map[string]any{
					"from": "Same",
				},
			},
		})
	}

	obj := New(ListenerSetGVK)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	obj.SetOwnerReferences([]metav1.OwnerReference{owner})

	obj.Object["spec"] = map[string]any{
		"parentRef": map[string]any{
			"group":     group,
			"kind":      "Gateway",
			"name":      parent.Name,
			"namespace": parent.Namespace,
		},
		"listeners": entries,
	}

	return obj, nil
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

	// Gateway API reads an omitted namespace as the object's own. Leaving it
	// empty would build a shard key no real Gateway matches, so the set's
	// listeners would count against nothing.
	if namespace == "" {
		namespace = obj.GetNamespace()
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
		if !found || err != nil || port < 1 || port > maxPort {
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

// ListenerStates reads status.listeners off a ListenerSet, keyed by listener
// name.
//
// Only conditions for the object's current generation count. Right after a
// listener moves to a new port, the status still describes the old one, and
// reading that as a fresh conflict would move it again.
func ListenerStates(obj *unstructured.Unstructured) map[string]ListenerState {
	state := map[string]ListenerState{}

	if obj == nil {
		return state
	}

	listeners, _, _ := unstructured.NestedSlice(obj.Object, "status", "listeners")
	generation := obj.GetGeneration()

	for _, raw := range listeners {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}

		name, _, _ := unstructured.NestedString(entry, "name")
		if name == "" {
			continue
		}

		conditions, _, _ := unstructured.NestedSlice(entry, "conditions")

		if status, found := currentCondition(conditions, "Conflicted", generation); found {
			if status == "True" {
				state[name] = ListenerConflicted
			} else {
				state[name] = ListenerOK
			}
			continue
		}

		if status, found := currentCondition(conditions, "Accepted", generation); found && status == "True" {
			state[name] = ListenerOK
			continue
		}

		state[name] = ListenerPending
	}

	return state
}
