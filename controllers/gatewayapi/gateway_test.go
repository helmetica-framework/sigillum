package gatewayapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// gatewayWithAddresses builds a Gateway whose status lists values in order.
func gatewayWithAddresses(values ...string) *gwv1.Gateway {
	gw := &gwv1.Gateway{}
	for _, v := range values {
		gw.Status.Addresses = append(gw.Status.Addresses, gwv1.GatewayStatusAddress{Value: v})
	}

	return gw
}

func TestGatewayAddress_TakesTheFirstAddress(t *testing.T) {
	addr, ok := GatewayAddress(gatewayWithAddresses("172.19.0.200", "172.19.0.201"))

	assert.True(t, ok)
	assert.Equal(t, "172.19.0.200", addr)
}

func TestGatewayAddress_NoStatusYet(t *testing.T) {
	addr, ok := GatewayAddress(&gwv1.Gateway{})

	assert.False(t, ok)
	assert.Empty(t, addr)
}

func TestGatewayAddress_EmptyValueIsNoAddress(t *testing.T) {
	_, ok := GatewayAddress(gatewayWithAddresses(""))

	assert.False(t, ok, "an empty value would publish tcp://:30007")
}

func TestGatewayAddress_NilIsNoAddress(t *testing.T) {
	_, ok := GatewayAddress(nil)

	assert.False(t, ok)
}
