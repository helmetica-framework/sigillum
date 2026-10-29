package gatewayapi

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

var groupVersion = schema.GroupVersion(gwv1.GroupVersion)

// Installed reports whether the cluster serves every Gateway API kind the
// controller watches.
func Installed(mapper meta.RESTMapper) (bool, error) {
	for _, kind := range []string{"Gateway", "ListenerSet", "TCPRoute"} {
		gk := groupVersion.WithKind(kind).GroupKind()
		_, err := mapper.RESTMapping(gk, groupVersion.Version)
		if meta.IsNoMatchError(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return true, nil
}
