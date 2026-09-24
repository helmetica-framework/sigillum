package gatewayapi

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

func registered(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, Register(scheme))

	return scheme
}

func TestRegister_KnowsEveryKind(t *testing.T) {
	scheme := registered(t)

	for _, gvk := range allGVKs {
		obj, err := scheme.New(gvk)

		require.NoError(t, err, "scheme must know %s", gvk)
		assert.IsType(t, &unstructured.Unstructured{}, obj)
	}
}

func TestRegister_KnowsEveryListKind(t *testing.T) {
	scheme := registered(t)

	for _, gvk := range allGVKs {
		obj, err := scheme.New(listKind(gvk))

		require.NoError(t, err, "scheme must know %s", listKind(gvk))
		assert.IsType(t, &unstructured.UnstructuredList{}, obj)
	}
}

func TestRegister_IsIdempotent(t *testing.T) {
	scheme := registered(t)

	assert.NotPanics(t, func() {
		require.NoError(t, Register(scheme))
	}, "registering the same type under the same GVK twice must be a no-op")
}

func TestGVKs_AreFullySpecified(t *testing.T) {
	for _, gvk := range allGVKs {
		assert.Equal(t, "gateway.networking.k8s.io", gvk.Group, "%s group", gvk.Kind)
		assert.Equal(t, "v1", gvk.Version, "%s version", gvk.Kind)
		assert.NotEmpty(t, gvk.Kind)
	}
}

// The lab cluster serves ListenerSet at gateway.networking.k8s.io/v1, not as
// XListenerSet under gateway.networking.x-k8s.io. Upstream Gateway API and the
// vendor docs disagree on this, so the value is pinned by a test.
func TestListenerSetGVK_MatchesTheCluster(t *testing.T) {
	assert.Equal(t, schema.GroupVersionKind{
		Group:   "gateway.networking.k8s.io",
		Version: "v1",
		Kind:    "ListenerSet",
	}, ListenerSetGVK)
}

func TestTCPRouteGVK_MatchesTheCluster(t *testing.T) {
	assert.Equal(t, schema.GroupVersionKind{
		Group:   "gateway.networking.k8s.io",
		Version: "v1",
		Kind:    "TCPRoute",
	}, TCPRouteGVK, "TCPRoute graduated to v1 in Gateway API 1.6; v1alpha2 is deprecated")
}

func TestAllGVKs_AreDistinct(t *testing.T) {
	seen := map[schema.GroupVersionKind]struct{}{}

	for _, gvk := range allGVKs {
		_, dup := seen[gvk]
		assert.False(t, dup, "%s listed twice", gvk)
		seen[gvk] = struct{}{}
	}

	assert.Len(t, seen, 4)
}

func TestNew_StampsTheGVK(t *testing.T) {
	obj := New(ListenerSetGVK)

	require.NotNil(t, obj)
	assert.Equal(t, ListenerSetGVK, obj.GroupVersionKind())
	assert.Equal(t, "gateway.networking.k8s.io/v1", obj.GetAPIVersion())
	assert.Equal(t, "ListenerSet", obj.GetKind())
}

func TestNew_ReturnsAnIndependentObject(t *testing.T) {
	first := New(ListenerSetGVK)
	second := New(ListenerSetGVK)

	first.SetName("mine")

	assert.Empty(t, second.GetName(), "New must not hand out a shared object")
}

func TestNew_HasNoFieldsBeyondTheGVK(t *testing.T) {
	obj := New(TCPRouteGVK)

	assert.Equal(t, []string{"apiVersion", "kind"}, sortedKeys(obj.Object))
}

func TestNewList_StampsTheListGVK(t *testing.T) {
	list := NewList(ListenerSetGVK)

	require.NotNil(t, list)
	assert.Equal(t, "ListenerSetList", list.GetKind())
	assert.Equal(t, "gateway.networking.k8s.io/v1", list.GetAPIVersion())
	assert.Empty(t, list.Items)
}

func TestListKind_AppendsList(t *testing.T) {
	assert.Equal(t, schema.GroupVersionKind{
		Group:   "gateway.networking.k8s.io",
		Version: "v1",
		Kind:    "GatewayList",
	}, listKind(GatewayGVK))
}

// scheme.New stores only the Go type, so it returns a blank unstructured rather
// than one carrying the GVK asked for. Everything in this package must go
// through New instead. If this ever starts failing, New can be simplified.
func TestSchemeNew_DoesNotStampTheGVK(t *testing.T) {
	scheme := registered(t)

	obj, err := scheme.New(ListenerSetGVK)
	require.NoError(t, err)

	assert.True(t, obj.GetObjectKind().GroupVersionKind().Empty())
}

// runtime.Scheme.ObjectKinds short-circuits for unstructured objects and reads
// the GVK off the object, which is why builder.Owns works without Register.
func TestGVKForObject_NeedsNoRegistration(t *testing.T) {
	empty := runtime.NewScheme()

	for _, gvk := range allGVKs {
		got, err := apiutil.GVKForObject(New(gvk), empty)

		require.NoError(t, err, "%s must resolve against an empty scheme", gvk)
		assert.Equal(t, gvk, got)
	}
}

// One Go type registered under many GVKs would break apiutil.GVKForObject for a
// normal struct. It is safe here only because of the unstructured
// short-circuit. Do not copy this pattern for typed objects.
func TestGVKForObject_ManyRegistrationsDoNotConfuseIt(t *testing.T) {
	scheme := registered(t)

	got, err := apiutil.GVKForObject(New(HTTPRouteGVK), scheme)

	require.NoError(t, err)
	assert.Equal(t, HTTPRouteGVK, got)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}
