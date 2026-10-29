package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	netv1 "k8s.io/api/networking/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
)

func TestReconcile_NoGatewayAPIFailsTheExposure(t *testing.T) {
	s := seal(1)
	s.Spec.Expose = []sealsv1.Exposure{tcpExposure("direct", 6379)}
	shard := tcpShard("tcp-0")
	m, c := newManager(s, exposableTCPClass(), &shard)
	m.NoGatewayAPI = true

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	got := &sealsv1.Seal{}
	require.NoError(t, c.Get(context.Background(), sealKey(), got))
	assert.Equal(t, sealsv1.SealPhaseFailed, got.Status.Phase)
	assert.Contains(t, got.Status.Message, errNoGatewayAPI.Error())
	require.Len(t, got.Status.Exposures, 1)
	assert.Equal(t, "direct", got.Status.Exposures[0].Name)
	assert.False(t, got.Status.Exposures[0].Ready)

	sets := &gwv1.ListenerSetList{}
	require.NoError(t, c.List(context.Background(), sets, client.InNamespace("default")))
	assert.Empty(t, sets.Items)
}

func TestReconcile_NoGatewayAPIStillWritesThePolicy(t *testing.T) {
	s := seal(1)
	s.Spec.AllowedNamespaces = []string{"other"}
	s.Spec.Expose = []sealsv1.Exposure{tcpExposure("direct", 6379)}
	m, c := newManager(s, exposableTCPClass())
	m.NoGatewayAPI = true

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	assert.Equal(t, []netv1.NetworkPolicyPeer{expectedNSPeer("other")}, ingressPeersOf(t, c),
		"no route means no gateway peer")
}

func TestReconcile_NoGatewayAPIWithoutExposuresIsReady(t *testing.T) {
	s := seal(1)
	s.Spec.AllowedNamespaces = []string{"other"}
	m, c := newManager(s)
	m.NoGatewayAPI = true

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	got := &sealsv1.Seal{}
	require.NoError(t, c.Get(context.Background(), sealKey(), got))
	assert.Equal(t, sealsv1.SealPhaseReady, got.Status.Phase)
	assert.Equal(t, "networkpolicy applied", got.Status.Message)
}
