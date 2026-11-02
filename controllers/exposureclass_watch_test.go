package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sigillumv1 "github.com/helmetica-framework/sigillum/api/v1"
)

func TestNetworkConfigsForClass_MatchesByName(t *testing.T) {
	s := networkConfig(1)
	s.Spec.Expose = []sigillumv1.Exposure{{Name: "direct", ExposureClass: "tcp", Protocol: sigillumv1.ProtocolTCP}}
	m, _ := newManager(s, tcpClass(0))

	got := m.networkConfigsForClass(context.Background(), tcpClass(0))

	require.Len(t, got, 1)
	assert.Equal(t, networkConfigKey(), got[0].NamespacedName)
}

func TestNetworkConfigsForClass_MatchesEmptyClassNameAgainstTheDefault(t *testing.T) {
	s := networkConfig(1)
	s.Spec.Expose = []sigillumv1.Exposure{{Name: "direct", Protocol: sigillumv1.ProtocolTCP}}
	m, _ := newManager(s, tcpClass(0))

	got := m.networkConfigsForClass(context.Background(), tcpClass(0))

	require.Len(t, got, 1, "an empty class name resolves to the default, so a default change must wake the network config")
	assert.Equal(t, networkConfigKey(), got[0].NamespacedName)
}

func TestNetworkConfigsForClass_DefaultForAnotherProtocolDoesNotWake(t *testing.T) {
	s := networkConfig(1)
	s.Spec.Expose = []sigillumv1.Exposure{{Name: "web", Protocol: sigillumv1.ProtocolHTTP}}
	m, _ := newManager(s, tcpClass(0))

	assert.Empty(t, m.networkConfigsForClass(context.Background(), tcpClass(0)),
		"an empty class name on HTTP resolves to the http class, not tcp")
}

func TestNetworkConfigsForClass_NonDefaultDoesNotWakeEmptyClassNames(t *testing.T) {
	s := networkConfig(1)
	s.Spec.Expose = []sigillumv1.Exposure{{Name: "direct", Protocol: sigillumv1.ProtocolTCP}}

	other := class("other", sigillumv1.ProtocolTCP)
	m, _ := newManager(s, &other)

	assert.Empty(t, m.networkConfigsForClass(context.Background(), &other))
}

func TestNetworkConfigsForClass_NoReferencesYieldsNoRequests(t *testing.T) {
	m, _ := newManager(networkConfig(1), tcpClass(0))

	assert.Empty(t, m.networkConfigsForClass(context.Background(), tcpClass(0)))
}

func TestNetworkConfigsForClass_TwoExposuresOnOneClassYieldOneRequest(t *testing.T) {
	s := networkConfig(1)
	s.Spec.Expose = []sigillumv1.Exposure{
		{Name: "a", ExposureClass: "tcp", Protocol: sigillumv1.ProtocolTCP},
		{Name: "b", ExposureClass: "tcp", Protocol: sigillumv1.ProtocolTCP},
	}
	m, _ := newManager(s, tcpClass(0))

	got := m.networkConfigsForClass(context.Background(), tcpClass(0))

	assert.Len(t, got, 1, "duplicate requests waste a reconcile")
}

func TestNetworkConfigsForClass_FoundByBothQueriesYieldsOneRequest(t *testing.T) {
	s := networkConfig(1)
	s.Spec.Expose = []sigillumv1.Exposure{
		{Name: "a", ExposureClass: "tcp", Protocol: sigillumv1.ProtocolTCP},
		{Name: "b", Protocol: sigillumv1.ProtocolTCP},
	}
	m, _ := newManager(s, tcpClass(0))

	got := m.networkConfigsForClass(context.Background(), tcpClass(0))

	assert.Len(t, got, 1, "the name query and the default query both find this network config")
}

func TestExposureClassIndexKeys_OneKeyPerReferencedClass(t *testing.T) {
	s := networkConfig(1)
	s.Spec.Expose = []sigillumv1.Exposure{
		{Name: "a", ExposureClass: "tcp", Protocol: sigillumv1.ProtocolTCP},
		{Name: "b", ExposureClass: "tcp", Protocol: sigillumv1.ProtocolTCP},
		{Name: "c", ExposureClass: "public", Protocol: sigillumv1.ProtocolHTTP},
	}

	keys := exposureClassIndexKeys(s)

	assert.ElementsMatch(t, []string{"tcp", "public"}, keys, "duplicate keys waste index space")
}

func TestExposureClassIndexKeys_EmptyClassNameIndexesAsEmpty(t *testing.T) {
	s := networkConfig(1)
	s.Spec.Expose = []sigillumv1.Exposure{{Name: "a", Protocol: sigillumv1.ProtocolTCP}}

	assert.Equal(t, []string{""}, exposureClassIndexKeys(s),
		"a default-taking exposure must be findable when a default class changes")
}

func TestExposureClassIndexKeys_NoExposuresYieldsNoKeys(t *testing.T) {
	assert.Empty(t, exposureClassIndexKeys(networkConfig(1)))
}

func TestExposureClassIndexKeys_NotANetworkConfigYieldsNoKeys(t *testing.T) {
	assert.Empty(t, exposureClassIndexKeys(tcpClass(0)))
}

func TestNetworkConfigControllerOptions_OneWorker(t *testing.T) {
	assert.Equal(t, 1, networkConfigControllerOptions().MaxConcurrentReconciles,
		"two workers interleave the port read and write and hand out the same port")
}
