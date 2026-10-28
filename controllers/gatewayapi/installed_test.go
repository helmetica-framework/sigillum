package gatewayapi

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func mapperWith(kinds ...string) *meta.DefaultRESTMapper {
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{groupVersion})
	for _, k := range kinds {
		m.Add(groupVersion.WithKind(k), meta.RESTScopeNamespace)
	}
	return m
}

func TestInstalled_AllKinds(t *testing.T) {
	ok, err := Installed(mapperWith("Gateway", "ListenerSet", "TCPRoute"))

	require.NoError(t, err)
	assert.True(t, ok)
}

func TestInstalled_NoKinds(t *testing.T) {
	ok, err := Installed(mapperWith())

	require.NoError(t, err)
	assert.False(t, ok)
}

func TestInstalled_MissingListenerSet(t *testing.T) {
	ok, err := Installed(mapperWith("Gateway", "TCPRoute"))

	require.NoError(t, err)
	assert.False(t, ok, "the controller owns ListenerSets, so it needs that CRD too")
}

type brokenMapper struct{ meta.RESTMapper }

func (brokenMapper) RESTMapping(schema.GroupKind, ...string) (*meta.RESTMapping, error) {
	return nil, errors.New("api server down")
}

func TestInstalled_OtherErrorsAreReturned(t *testing.T) {
	_, err := Installed(brokenMapper{})

	assert.Error(t, err)
}
