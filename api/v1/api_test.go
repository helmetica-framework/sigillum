package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
)

func TestSchemeRegistration(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))

	assert.True(t, scheme.Recognizes(GroupVersion.WithKind("Seal")))
	assert.True(t, scheme.Recognizes(GroupVersion.WithKind("SealList")))
	assert.Equal(t, "seals.helmetica.io", GroupVersion.Group)
	assert.Equal(t, "v1", GroupVersion.Version)
}

func TestSealDeepCopy(t *testing.T) {
	orig := &Seal{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "sample",
			Namespace:  "default",
			Generation: 3,
			Labels:     map[string]string{"key": "value"},
		},
		Spec: SealSpec{
			AllowedNamespaces:  []string{"some-value"},
			AllowAllNamespaces: ptr.To(true),
		},
		Status: SealStatus{
			Phase:              SealPhaseReady,
			ObservedGeneration: 3,
		},
	}

	cp := orig.DeepCopy()
	require.Equal(t, orig, cp)

	cp.Labels["key"] = "mutated"
	assert.Equal(t, "value", orig.Labels["key"], "deepcopy must not share the labels map")

	cp.Spec.AllowedNamespaces[0] = "mutated"
	assert.Equal(t, "some-value", orig.Spec.AllowedNamespaces[0],
		"deepcopy must not share the allowedNamespaces slice")

	*cp.Spec.AllowAllNamespaces = false
	assert.True(t, *orig.Spec.AllowAllNamespaces,
		"deepcopy must not share the allowAllNamespaces pointer")
}

func TestSealListDeepCopy(t *testing.T) {
	orig := &SealList{
		Items: []Seal{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "one"},
				Spec:       SealSpec{AllowedNamespaces: []string{"a"}},
			},
		},
	}

	cp := orig.DeepCopy()
	require.Equal(t, orig, cp)

	cp.Items[0].Spec.AllowedNamespaces[0] = "mutated"
	assert.Equal(t, "a", orig.Items[0].Spec.AllowedNamespaces[0],
		"deepcopy must not share the items slice")
}
