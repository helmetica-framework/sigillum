package gatewayapi

import gwv1 "sigs.k8s.io/gateway-api/apis/v1"

// GatewayAddress reads the first address off a Gateway's status. False means
// the gateway controller has not assigned one yet.
func GatewayAddress(gw *gwv1.Gateway) (string, bool) {
	if gw == nil || len(gw.Status.Addresses) == 0 {
		return "", false
	}

	value := gw.Status.Addresses[0].Value
	return value, value != ""
}
