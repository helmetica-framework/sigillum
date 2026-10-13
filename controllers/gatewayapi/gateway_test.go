package gatewayapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// gatewayWithAddresses builds a Gateway whose status lists values in order.
func gatewayWithAddresses(t *testing.T, values ...string) *unstructured.Unstructured {
	t.Helper()

	obj := New(GatewayGVK)
	obj.SetName("tcp-0")
	obj.SetNamespace("kgateway-system")

	addrs := make([]any, 0, len(values))
	for _, v := range values {
		addrs = append(addrs, map[string]any{"type": "IPAddress", "value": v})
	}
	require.NoError(t, unstructured.SetNestedSlice(obj.Object, addrs, "status", "addresses"))

	return obj
}

func TestGatewayAddress_TakesTheFirstAddress(t *testing.T) {
	addr, ok := GatewayAddress(gatewayWithAddresses(t, "172.19.0.200", "172.19.0.201"))

	assert.True(t, ok)
	assert.Equal(t, "172.19.0.200", addr)
}

func TestGatewayAddress_NoStatusYet(t *testing.T) {
	obj := New(GatewayGVK)

	addr, ok := GatewayAddress(obj)

	assert.False(t, ok)
	assert.Empty(t, addr)
}

func TestGatewayAddress_EmptyListIsNoAddress(t *testing.T) {
	_, ok := GatewayAddress(gatewayWithAddresses(t))

	assert.False(t, ok)
}

func TestGatewayAddress_EmptyValueIsNoAddress(t *testing.T) {
	_, ok := GatewayAddress(gatewayWithAddresses(t, ""))

	assert.False(t, ok, "an empty value would publish tcp://:30007")
}

func TestGatewayAddress_NilIsNoAddress(t *testing.T) {
	_, ok := GatewayAddress(nil)

	assert.False(t, ok)
}
