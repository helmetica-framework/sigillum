package gatewayapi

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwac "sigs.k8s.io/gateway-api/applyconfiguration/apis/v1"
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

// BuildListenerSet renders a ListenerSet parented to a shard Gateway.
//
// A nil result with no error means there is nothing to write: spec.listeners
// requires at least one entry, so the caller deletes the object instead.
//
// An error means the caller passed something that cannot be written. That is a
// bug in the caller, not a cluster state to reconcile, so it is never a nil
// result: the caller must not read it as "delete".
func BuildListenerSet(
	name, namespace string,
	parent ShardRef,
	listeners []Listener,
	owner *metav1ac.OwnerReferenceApplyConfiguration,
) (*gwac.ListenerSetApplyConfiguration, error) {
	if len(listeners) == 0 {
		return nil, nil
	}

	// A nameless parent reads back as no parent at all, so this set's ports
	// would count against no shard and be handed to a second Seal.
	if parent.Name == "" {
		return nil, fmt.Errorf("listenerset %s/%s: parent shard has no name", namespace, name)
	}

	entries := make([]*gwac.ListenerEntryApplyConfiguration, 0, len(listeners))

	for _, l := range listeners {
		if l.Port < 1 || l.Port > maxPort {
			return nil, fmt.Errorf("listenerset %s/%s: listener %q port %d is outside 1-%d",
				namespace, name, l.Name, l.Port, maxPort)
		}

		entries = append(entries, gwac.ListenerEntry().
			WithName(gwv1.SectionName(l.Name)).
			WithProtocol(gwv1.ProtocolType(l.Protocol)).
			WithPort(l.Port).
			WithAllowedRoutes(gwac.AllowedRoutes().
				WithNamespaces(gwac.RouteNamespaces().WithFrom(gwv1.NamespacesFromSame))))
	}

	return gwac.ListenerSet(name, namespace).
		WithOwnerReferences(owner).
		WithSpec(gwac.ListenerSetSpec().
			WithParentRef(gwac.ParentGatewayReference().
				WithGroup(gwv1.GroupName).
				WithKind("Gateway").
				WithName(gwv1.ObjectName(parent.Name)).
				WithNamespace(gwv1.Namespace(parent.Namespace))).
			WithListeners(entries...)), nil
}

// ParentShard reads spec.parentRef off a ListenerSet. The second result is
// false when the object carries no usable parent.
func ParentShard(ls *gwv1.ListenerSet) (ShardRef, bool) {
	// A parentless set must not silently count against some shard.
	if ls == nil || ls.Spec.ParentRef.Name == "" {
		return ShardRef{}, false
	}

	// Gateway API reads an omitted namespace as the object's own. Leaving it
	// empty would build a shard key no real Gateway matches, so the set's
	// listeners would count against nothing.
	namespace := ls.Namespace
	if ns := ls.Spec.ParentRef.Namespace; ns != nil && *ns != "" {
		namespace = string(*ns)
	}

	return ShardRef{Name: string(ls.Spec.ParentRef.Name), Namespace: namespace}, true
}

// Listeners reads spec.listeners off a ListenerSet.
//
// A port outside 1..maxPort is skipped. It cannot be a real claim, and the
// builder never writes one.
func Listeners(ls *gwv1.ListenerSet) []Listener {
	if ls == nil {
		return nil
	}

	listeners := []Listener{}

	for _, l := range ls.Spec.Listeners {
		if l.Port < 1 || l.Port > maxPort {
			continue
		}

		listeners = append(listeners, Listener{
			Name:     string(l.Name),
			Protocol: string(l.Protocol),
			Port:     int32(l.Port),
		})
	}

	return listeners
}

// ListenerStates reads status.listeners off a ListenerSet, keyed by listener
// name.
//
// Only conditions for the object's current generation count. Right after a
// listener moves to a new port, the status still describes the old one, and
// reading that as a fresh conflict would move it again.
func ListenerStates(ls *gwv1.ListenerSet) map[string]ListenerState {
	state := map[string]ListenerState{}

	if ls == nil {
		return state
	}

	for _, l := range ls.Status.Listeners {
		if l.Name == "" {
			continue
		}

		name := string(l.Name)

		if c, found := currentCondition(l.Conditions, string(gwv1.ListenerEntryConditionConflicted), ls.Generation); found {
			if c.Status == metav1.ConditionTrue {
				state[name] = ListenerConflicted
			} else {
				state[name] = ListenerOK
			}
			continue
		}

		if c, found := currentCondition(l.Conditions, string(gwv1.ListenerEntryConditionAccepted), ls.Generation); found && c.Status == metav1.ConditionTrue {
			state[name] = ListenerOK
			continue
		}

		state[name] = ListenerPending
	}

	return state
}
