package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	"github.com/helmetica-framework/sigillum/controllers/gatewayapi"
)

func TestControllerCmd_LeaderElectionDefaultsToOn(t *testing.T) {
	v, err := controllerCmd.Flags().GetBool("leader-elect")

	require.NoError(t, err)
	assert.True(t, v,
		"two replicas without a leader allocate the same port, defeating the live read")
}

func TestNewScheme_KnowsTheGatewayKinds(t *testing.T) {
	scheme := newScheme()

	for _, gvk := range []schema.GroupVersionKind{
		gatewayapi.ListenerSetGVK, gatewayapi.TCPRouteGVK,
		gatewayapi.HTTPRouteGVK, gatewayapi.GatewayGVK,
	} {
		assert.True(t, scheme.Recognizes(gvk), "scheme must recognise %s", gvk)
	}
}

func TestNewScheme_StillKnowsSeal(t *testing.T) {
	scheme := newScheme()

	assert.True(t, scheme.Recognizes(sealsv1.GroupVersion.WithKind("Seal")))
	assert.True(t, scheme.Recognizes(sealsv1.GroupVersion.WithKind("ExposureClass")))
}
