package v1

import (
	"reflect"
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

func TestExposureClassSchemeRegistration(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))

	assert.True(t, scheme.Recognizes(GroupVersion.WithKind("ExposureClass")))
	assert.True(t, scheme.Recognizes(GroupVersion.WithKind("ExposureClassList")))
}

func sealWithExposures() *Seal {
	return &Seal{
		ObjectMeta: metav1.ObjectMeta{Name: "networkconfig", Namespace: "redis-7f3"},
		Spec: SealSpec{
			AllowedNamespaces: []string{"orders"},
			Expose: []Exposure{
				{
					Name:     "api",
					Protocol: ProtocolHTTP,
					Backend:  Backend{Service: "redis", Port: 6379},
				},
				{
					Name:          "direct",
					ExposureClass: "myClass",
					Protocol:      ProtocolTCP,
					Backend:       Backend{Service: "redis", Port: 6379},
				},
			},
		},
		Status: SealStatus{
			Phase: SealPhaseReady,
			Exposures: []ExposureStatus{
				{Name: "api", Class: "public", Ready: true, Hostname: "redis-7f3.apps.example.com"},
				{Name: "direct", Class: "tcp", Ready: true, Shard: "tcp-0", Address: "172.19.0.200", Port: 30007},
			},
		},
	}
}

func TestSealWithExposuresDeepCopy(t *testing.T) {
	orig := sealWithExposures()

	cp := orig.DeepCopy()
	require.Equal(t, orig, cp)

	cp.Spec.Expose[0].Backend.Port = 1
	cp.Status.Exposures[1].Port = 2

	assert.EqualValues(t, 6379, orig.Spec.Expose[0].Backend.Port,
		"deepcopy must not share the expose slice")
	assert.EqualValues(t, 30007, orig.Status.Exposures[1].Port,
		"deepcopy must not share the exposures slice")
}

func TestSealNilExposuresDeepCopyStaysNil(t *testing.T) {
	orig := &Seal{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "n"}}

	cp := orig.DeepCopy()

	assert.Nil(t, cp.Spec.Expose, "a nil slice must not become an empty one")
	assert.Nil(t, cp.Status.Exposures)
}

// A user must not be able to pin a TCP port. The guarantee is that no spec
// type carries a port the controller would read, so it is checked here rather
// than left to a code review.
func TestExposureSpecHasNoAllocatablePortField(t *testing.T) {
	typ := reflect.TypeOf(Exposure{})

	for i := range typ.NumField() {
		assert.NotEqual(t, "Port", typ.Field(i).Name,
			"a port on Exposure would let a user pick one, which is exactly what this controller prevents")
	}
}

func exposureClass() *ExposureClass {
	return &ExposureClass{
		ObjectMeta: metav1.ObjectMeta{Name: "tcp"},
		Spec: ExposureClassSpec{
			Type:     ExposureTypeGateway,
			Supports: []Protocol{ProtocolTCP},
			ShardSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"sigillum.io/shard-pool": "tcp"},
			},
			PortRange:            &PortRange{From: 30000, To: 30255},
			MaxListenersPerShard: 64,
			GatewaySelector: &GatewaySelector{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kgateway-system"},
				},
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app.kubernetes.io/name": "kgateway"},
				},
			},
		},
	}
}

func TestExposureClassDeepCopy(t *testing.T) {
	orig := exposureClass()

	cp := orig.DeepCopy()
	require.Equal(t, orig, cp)

	cp.Spec.PortRange.From = 1
	cp.Spec.Supports[0] = ProtocolHTTP
	cp.Spec.ShardSelector.MatchLabels["sigillum.io/shard-pool"] = "other"
	cp.Spec.GatewaySelector.PodSelector.MatchLabels["app.kubernetes.io/name"] = "other"

	assert.EqualValues(t, 30000, orig.Spec.PortRange.From,
		"deepcopy must not share the portRange pointer")
	assert.Equal(t, ProtocolTCP, orig.Spec.Supports[0],
		"deepcopy must not share the supports slice")
	assert.Equal(t, "tcp", orig.Spec.ShardSelector.MatchLabels["sigillum.io/shard-pool"],
		"deepcopy must not share the shardSelector map")
	assert.Equal(t, "kgateway", orig.Spec.GatewaySelector.PodSelector.MatchLabels["app.kubernetes.io/name"],
		"deepcopy must not share the nested gatewaySelector maps")
}

func TestExposureClassHTTPFieldsDeepCopy(t *testing.T) {
	orig := &ExposureClass{
		ObjectMeta: metav1.ObjectMeta{Name: "public"},
		Spec: ExposureClassSpec{
			Type:      ExposureTypeGateway,
			Default:   ptr.To(true),
			Supports:  []Protocol{ProtocolHTTP, ProtocolHTTPS},
			ParentRef: &ParentRef{Name: "web", Namespace: "kgateway-system"},
			Domain:    "{{.Subdomain}}{{.Instance}}.apps.example.com",
			TLS: &ExposureTLS{
				Mode:      TLSModeGatewayTerminated,
				IssuerRef: &IssuerRef{Name: "letsencrypt", Kind: "ClusterIssuer"},
			},
		},
	}

	cp := orig.DeepCopy()
	require.Equal(t, orig, cp)

	*cp.Spec.Default = false
	cp.Spec.ParentRef.Name = "mutated"
	cp.Spec.TLS.IssuerRef.Name = "mutated"

	assert.True(t, *orig.Spec.Default, "deepcopy must not share the default pointer")
	assert.Equal(t, "web", orig.Spec.ParentRef.Name, "deepcopy must not share the parentRef pointer")
	assert.Equal(t, "letsencrypt", orig.Spec.TLS.IssuerRef.Name, "deepcopy must not share the issuerRef pointer")
}

func TestExposureClassListDeepCopy(t *testing.T) {
	orig := &ExposureClassList{Items: []ExposureClass{*exposureClass()}}

	cp := orig.DeepCopy()
	require.Equal(t, orig, cp)

	cp.Items[0].Spec.MaxListenersPerShard = 1

	assert.EqualValues(t, 64, orig.Items[0].Spec.MaxListenersPerShard,
		"deepcopy must not share the items slice")
}
