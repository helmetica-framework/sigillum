package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
)

func TestSealsForClass_MatchesByName(t *testing.T) {
	s := seal(1)
	s.Spec.Expose = []sealsv1.Exposure{{Name: "direct", ExposureClass: "tcp", Protocol: sealsv1.ProtocolTCP}}
	m, _ := newManager(s, tcpClass(0))

	got := m.sealsForClass(context.Background(), tcpClass(0))

	require.Len(t, got, 1)
	assert.Equal(t, sealKey(), got[0].NamespacedName)
}

func TestSealsForClass_MatchesEmptyClassNameAgainstTheDefault(t *testing.T) {
	s := seal(1)
	s.Spec.Expose = []sealsv1.Exposure{{Name: "direct", Protocol: sealsv1.ProtocolTCP}}
	m, _ := newManager(s, tcpClass(0))

	got := m.sealsForClass(context.Background(), tcpClass(0))

	require.Len(t, got, 1, "an empty class name resolves to the default, so a default change must wake the seal")
	assert.Equal(t, sealKey(), got[0].NamespacedName)
}

func TestSealsForClass_DefaultForAnotherProtocolDoesNotWake(t *testing.T) {
	s := seal(1)
	s.Spec.Expose = []sealsv1.Exposure{{Name: "web", Protocol: sealsv1.ProtocolHTTP}}
	m, _ := newManager(s, tcpClass(0))

	assert.Empty(t, m.sealsForClass(context.Background(), tcpClass(0)),
		"an empty class name on HTTP resolves to the http class, not tcp")
}

func TestSealsForClass_NonDefaultDoesNotWakeEmptyClassNames(t *testing.T) {
	s := seal(1)
	s.Spec.Expose = []sealsv1.Exposure{{Name: "direct", Protocol: sealsv1.ProtocolTCP}}

	other := class("other", sealsv1.ProtocolTCP)
	m, _ := newManager(s, &other)

	assert.Empty(t, m.sealsForClass(context.Background(), &other))
}

func TestSealsForClass_NoReferencesYieldsNoRequests(t *testing.T) {
	m, _ := newManager(seal(1), tcpClass(0))

	assert.Empty(t, m.sealsForClass(context.Background(), tcpClass(0)))
}

func TestSealsForClass_TwoExposuresOnOneClassYieldOneRequest(t *testing.T) {
	s := seal(1)
	s.Spec.Expose = []sealsv1.Exposure{
		{Name: "a", ExposureClass: "tcp", Protocol: sealsv1.ProtocolTCP},
		{Name: "b", ExposureClass: "tcp", Protocol: sealsv1.ProtocolTCP},
	}
	m, _ := newManager(s, tcpClass(0))

	got := m.sealsForClass(context.Background(), tcpClass(0))

	assert.Len(t, got, 1, "duplicate requests waste a reconcile")
}

func TestSealsForClass_FoundByBothQueriesYieldsOneRequest(t *testing.T) {
	s := seal(1)
	s.Spec.Expose = []sealsv1.Exposure{
		{Name: "a", ExposureClass: "tcp", Protocol: sealsv1.ProtocolTCP},
		{Name: "b", Protocol: sealsv1.ProtocolTCP},
	}
	m, _ := newManager(s, tcpClass(0))

	got := m.sealsForClass(context.Background(), tcpClass(0))

	assert.Len(t, got, 1, "the name query and the default query both find this seal")
}

func TestExposureClassIndexKeys_OneKeyPerReferencedClass(t *testing.T) {
	s := seal(1)
	s.Spec.Expose = []sealsv1.Exposure{
		{Name: "a", ExposureClass: "tcp", Protocol: sealsv1.ProtocolTCP},
		{Name: "b", ExposureClass: "tcp", Protocol: sealsv1.ProtocolTCP},
		{Name: "c", ExposureClass: "public", Protocol: sealsv1.ProtocolHTTP},
	}

	keys := exposureClassIndexKeys(s)

	assert.ElementsMatch(t, []string{"tcp", "public"}, keys, "duplicate keys waste index space")
}

func TestExposureClassIndexKeys_EmptyClassNameIndexesAsEmpty(t *testing.T) {
	s := seal(1)
	s.Spec.Expose = []sealsv1.Exposure{{Name: "a", Protocol: sealsv1.ProtocolTCP}}

	assert.Equal(t, []string{""}, exposureClassIndexKeys(s),
		"a default-taking exposure must be findable when a default class changes")
}

func TestExposureClassIndexKeys_NoExposuresYieldsNoKeys(t *testing.T) {
	assert.Empty(t, exposureClassIndexKeys(seal(1)))
}

func TestExposureClassIndexKeys_NotASealYieldsNoKeys(t *testing.T) {
	assert.Empty(t, exposureClassIndexKeys(tcpClass(0)))
}

func TestSealControllerOptions_OneWorker(t *testing.T) {
	assert.Equal(t, 1, sealControllerOptions().MaxConcurrentReconciles,
		"two workers interleave the port read and write and hand out the same port")
}
