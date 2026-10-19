package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
)

func TestControllerCmd_LeaderElectionDefaultsToOn(t *testing.T) {
	v, err := controllerCmd.Flags().GetBool("leader-elect")

	require.NoError(t, err)
	assert.True(t, v,
		"two replicas without a leader allocate the same port, defeating the live read")
}

func TestNewScheme_KnowsTheGatewayKinds(t *testing.T) {
	scheme := newScheme()

	for _, kind := range []string{"ListenerSet", "TCPRoute", "HTTPRoute", "Gateway"} {
		gvk := gwv1.SchemeGroupVersion.WithKind(kind)
		assert.True(t, scheme.Recognizes(gvk), "scheme must recognise %s", gvk)
	}
}

func TestNewScheme_StillKnowsSeal(t *testing.T) {
	scheme := newScheme()

	assert.True(t, scheme.Recognizes(sealsv1.GroupVersion.WithKind("Seal")))
	assert.True(t, scheme.Recognizes(sealsv1.GroupVersion.WithKind("ExposureClass")))
}
