package gatewayapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
// The parent is the ListenerSet, not the Gateway, with sectionName set to the
// listener name, so the route binds to exactly one port.
//
// It errors rather than writing a route the gateway controller would reject.
func BuildTCPRoute(
	sealName, namespace, exposureName string,
	backend Backend,
	owner metav1.OwnerReference,
) (*unstructured.Unstructured, error) {
	if backend.Service == "" {
		return nil, fmt.Errorf("exposure %q: backend service is empty", exposureName)
	}

	if backend.Port < 1 || backend.Port > maxPort {
		return nil, fmt.Errorf("exposure %q: backend port %d is outside 1-%d",
			exposureName, backend.Port, maxPort)
	}

	route := New(TCPRouteGVK)
	route.SetName(RouteName(sealName, exposureName))
	route.SetNamespace(namespace)
	route.SetOwnerReferences([]metav1.OwnerReference{owner})

	route.Object["spec"] = map[string]any{
		"parentRefs": []any{
			map[string]any{
				"group":       ListenerSetGVK.Group,
				"kind":        ListenerSetGVK.Kind,
				"name":        sealName,
				"sectionName": exposureName,
			},
		},
		"rules": []any{
			map[string]any{
				"backendRefs": []any{
					map[string]any{
						"name": backend.Service,
						"port": int64(backend.Port),
					},
				},
			},
		},
	}

	return route, nil
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
func RouteIsReady(obj *unstructured.Unstructured) (bool, string) {
	const notSeen = "waiting for the gateway controller to accept the route"

	if obj == nil {
		return false, notSeen
	}

	parents, _, _ := unstructured.NestedSlice(obj.Object, "status", "parents")

	msg := ""
	for _, rawp := range parents {
		p, ok := rawp.(map[string]any)
		if !ok {
			continue
		}

		conds, _, _ := unstructured.NestedSlice(p, "conditions")

		ready := true
		for _, condType := range []string{"Accepted", "ResolvedRefs"} {
			c, found := findCurrentCondition(conds, condType, obj.GetGeneration())
			if !found {
				ready = false
				continue
			}

			status, _, _ := unstructured.NestedString(c, "status")
			if status == "True" {
				continue
			}

			ready = false
			if msg == "" {
				msg, _, _ = unstructured.NestedString(c, "message")
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
