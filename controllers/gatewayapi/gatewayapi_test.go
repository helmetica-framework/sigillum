package gatewayapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// The lab cluster serves these at gateway.networking.k8s.io/v1. Upstream
// Gateway API and the vendor docs disagree on this, so the values are pinned
// by a test.
func TestRegister_KnowsEveryKindAtV1(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, Register(scheme))

	for _, kind := range []string{"ListenerSet", "TCPRoute", "Gateway"} {
		gvk := gwv1.SchemeGroupVersion.WithKind(kind)

		assert.Equal(t, "gateway.networking.k8s.io", gvk.Group)
		assert.Equal(t, "v1", gvk.Version)
		assert.True(t, scheme.Recognizes(gvk), "scheme must know %s", gvk)
		assert.True(t, scheme.Recognizes(gwv1.SchemeGroupVersion.WithKind(kind+"List")))
	}
}
