package gatewayapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwac "sigs.k8s.io/gateway-api/applyconfiguration/apis/v1"
)

// maxNameLength is the longest name a Kubernetes object can carry.
const maxNameLength = 253

// hashLength is how many hex characters of the hash go on a truncated name.
const hashLength = 8

// Backend names the in-namespace service a route sends traffic to.
type Backend struct {
	Service string
	Port    int32
}

// BuildTCPRoute renders a TCPRoute binding one listener to one backend service.
//
// The parent is the exposure's ListenerSet, not the Gateway, with sectionName
// set to the listener name, so the route binds to exactly one port.
//
// It errors rather than writing a route the gateway controller would reject.
func BuildTCPRoute(
	sealName, namespace, exposureName string,
	backend Backend,
	owner *metav1ac.OwnerReferenceApplyConfiguration,
) (*gwac.TCPRouteApplyConfiguration, error) {
	if backend.Service == "" {
		return nil, fmt.Errorf("exposure %q: backend service is empty", exposureName)
	}

	if backend.Port < 1 || backend.Port > maxPort {
		return nil, fmt.Errorf("exposure %q: backend port %d is outside 1-%d",
			exposureName, backend.Port, maxPort)
	}

	return gwac.TCPRoute(RouteName(sealName, exposureName), namespace).
		WithOwnerReferences(owner).
		WithSpec(gwac.TCPRouteSpec().
			WithParentRefs(gwac.ParentReference().
				WithGroup(gwv1.GroupName).
				WithKind("ListenerSet").
				WithName(gwv1.ObjectName(ListenerSetName(sealName, exposureName))).
				WithSectionName(gwv1.SectionName(exposureName))).
			WithRules(gwac.TCPRouteRule().
				WithBackendRefs(gwac.BackendRef().
					WithName(gwv1.ObjectName(backend.Service)).
					WithPort(backend.Port)))), nil
}

// RouteName is the name of the TCPRoute for one exposure.
//
// Seal and exposure names are each valid on their own but can exceed the object
// name limit once joined, so a long result is truncated and given a hash suffix
// to keep it unique.
func RouteName(sealName, exposureName string) string {
	name := sealName + "-" + exposureName
	if len(name) <= maxNameLength {
		return name
	}

	sum := sha256.Sum256([]byte(name))
	suffix := hex.EncodeToString(sum[:])[:hashLength]
	prefix := strings.TrimRight(name[:maxNameLength-hashLength-1], "-.")

	return prefix + "-" + suffix
}

// RouteIsReady reports whether the gateway controller both accepted the route
// and resolved its backend. When not, it returns the route's own condition
// message, so the user sees what the gateway controller said.
func RouteIsReady(route *gwv1.TCPRoute) (bool, string) {
	const notSeen = "waiting for the gateway controller to accept the route"

	if route == nil {
		return false, notSeen
	}

	msg := ""
	for _, p := range route.Status.Parents {
		ready := true
		for _, condType := range []gwv1.RouteConditionType{gwv1.RouteConditionAccepted, gwv1.RouteConditionResolvedRefs} {
			c, found := currentCondition(p.Conditions, string(condType), route.Generation)
			if !found {
				ready = false
				continue
			}

			if c.Status == metav1.ConditionTrue {
				continue
			}

			ready = false
			if msg == "" {
				msg = c.Message
			}
		}

		if ready {
			return true, ""
		}
	}

	if msg == "" {
		msg = notSeen
	}
	return false, msg
}
