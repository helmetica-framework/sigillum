package controllers

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
)

func class(name string, protocols ...sealsv1.Protocol) sealsv1.ExposureClass {
	return sealsv1.ExposureClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: sealsv1.ExposureClassSpec{
			Type:     sealsv1.ExposureTypeGateway,
			Supports: protocols,
		},
	}
}

func exposure(className string, p sealsv1.Protocol) sealsv1.Exposure {
	return sealsv1.Exposure{
		Name:          "direct",
		ExposureClass: className,
		Protocol:      p,
		Backend:       sealsv1.Backend{Service: "redis", Port: 6379},
	}
}

func TestDefaultClassName_LowercasesTheProtocol(t *testing.T) {
	assert.Equal(t, "tcp", defaultClassName(sealsv1.ProtocolTCP))
	assert.Equal(t, "http", defaultClassName(sealsv1.ProtocolHTTP))
	assert.Equal(t, "https", defaultClassName(sealsv1.ProtocolHTTPS))
}

func TestDefaultClassName_IsAValidObjectName(t *testing.T) {
	// Object names are lowercase DNS labels. A name with upper case in it
	// could never match a real ExposureClass, so the lookup would always miss.
	for _, p := range []sealsv1.Protocol{sealsv1.ProtocolTCP, sealsv1.ProtocolHTTP, sealsv1.ProtocolHTTPS} {
		name := defaultClassName(p)

		assert.Regexp(t, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, name, "protocol %s", p)
	}
}

func TestResolveClass_NamedClass(t *testing.T) {
	classes := []sealsv1.ExposureClass{
		class("http", sealsv1.ProtocolHTTP),
		class("myClass", sealsv1.ProtocolTCP),
	}

	got, err := resolveClass(classes, exposure("myClass", sealsv1.ProtocolTCP))

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "myClass", got.Name)
}

func TestResolveClass_NamedClassWinsOverTheProtocolName(t *testing.T) {
	classes := []sealsv1.ExposureClass{
		class("tcp", sealsv1.ProtocolTCP),
		class("myClass", sealsv1.ProtocolTCP),
	}

	got, err := resolveClass(classes, exposure("myClass", sealsv1.ProtocolTCP))

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "myClass", got.Name, "naming a class must override the protocol fallback")
}

func TestResolveClass_EmptyNameFallsBackToTheProtocolName(t *testing.T) {
	classes := []sealsv1.ExposureClass{
		class("http", sealsv1.ProtocolHTTP),
		class("tcp", sealsv1.ProtocolTCP),
	}

	got, err := resolveClass(classes, exposure("", sealsv1.ProtocolTCP))

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "tcp", got.Name)
}

func TestResolveClass_HTTPAndHTTPSAreSeparateClasses(t *testing.T) {
	classes := []sealsv1.ExposureClass{
		class("http", sealsv1.ProtocolHTTP),
		class("https", sealsv1.ProtocolHTTPS),
	}

	plain, err := resolveClass(classes, exposure("", sealsv1.ProtocolHTTP))
	require.NoError(t, err)
	require.NotNil(t, plain)
	assert.Equal(t, "http", plain.Name)

	secure, err := resolveClass(classes, exposure("", sealsv1.ProtocolHTTPS))
	require.NoError(t, err)
	require.NotNil(t, secure)
	assert.Equal(t, "https", secure.Name, "https must not silently fall back to the http class")
}

func TestResolveClass_NamedClassMissing(t *testing.T) {
	classes := []sealsv1.ExposureClass{class("http", sealsv1.ProtocolHTTP)}

	_, err := resolveClass(classes, exposure("nope", sealsv1.ProtocolTCP))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope", "the message must name the class the user asked for")
}

func TestResolveClass_ProtocolClassMissingNamesIt(t *testing.T) {
	classes := []sealsv1.ExposureClass{class("http", sealsv1.ProtocolHTTP)}

	_, err := resolveClass(classes, exposure("", sealsv1.ProtocolTCP))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "tcp",
		"the user never typed a class name, so the message has to say which one was looked for")
}

// A deleted class must not tear down the objects it already produced, so
// the caller has to tell "gone" apart from "wrong".
func TestResolveClass_MissingNamedClassIsDistinguishable(t *testing.T) {
	_, err := resolveClass(nil, exposure("deleted", sealsv1.ProtocolTCP))

	require.Error(t, err)
	assert.True(t, errors.Is(err, errClassNotFound))
}

func TestResolveClass_MissingProtocolClassIsAlsoDistinguishable(t *testing.T) {
	_, err := resolveClass(nil, exposure("", sealsv1.ProtocolTCP))

	require.Error(t, err)
	assert.True(t, errors.Is(err, errClassNotFound),
		"a deleted tcp class is just as much a deletion as a deleted named one")
}

func TestResolveClass_NamedClassDoesNotSupportTheProtocol(t *testing.T) {
	classes := []sealsv1.ExposureClass{class("http", sealsv1.ProtocolHTTP)}

	_, err := resolveClass(classes, exposure("http", sealsv1.ProtocolTCP))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "http")
	assert.Contains(t, err.Error(), "TCP")
	assert.False(t, errors.Is(err, errClassNotFound),
		"the class exists, it is just wrong, so this must not read as a deletion")
}

// A class named after a protocol still has to list it. Trusting the name alone
// would let a class called "tcp" that only supports HTTP through.
func TestResolveClass_ProtocolClassMustStillListTheProtocol(t *testing.T) {
	classes := []sealsv1.ExposureClass{class("tcp", sealsv1.ProtocolHTTP)}

	_, err := resolveClass(classes, exposure("", sealsv1.ProtocolTCP))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "TCP")
}

func TestResolveClass_EmptyClassList(t *testing.T) {
	_, err := resolveClass(nil, exposure("", sealsv1.ProtocolHTTP))

	require.Error(t, err)
}

func TestResolveClass_RejectsAnUnusableNamedClass(t *testing.T) {
	c := class("legacy", sealsv1.ProtocolHTTP)
	c.Spec.Type = "Ingress"

	_, err := resolveClass([]sealsv1.ExposureClass{c}, exposure("legacy", sealsv1.ProtocolHTTP))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Ingress")
}

func TestResolveClass_UnusableProtocolClassReportsWhy(t *testing.T) {
	c := class("http", sealsv1.ProtocolHTTP)
	c.Spec.Type = "Ingress"

	_, err := resolveClass([]sealsv1.ExposureClass{c}, exposure("", sealsv1.ProtocolHTTP))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Ingress",
		"'not found' would send the user looking for a class that is right there")
	assert.False(t, errors.Is(err, errClassNotFound))
}

func TestResolveClass_ReturnsTheClassNotACopy(t *testing.T) {
	classes := []sealsv1.ExposureClass{class("tcp", sealsv1.ProtocolTCP)}

	got, err := resolveClass(classes, exposure("tcp", sealsv1.ProtocolTCP))

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Same(t, &classes[0], got, "callers read big selectors off this, so it must not be copied per exposure")
}

func TestSupports_FindsTheProtocol(t *testing.T) {
	c := class("public", sealsv1.ProtocolHTTP, sealsv1.ProtocolHTTPS)

	assert.True(t, supports(c, sealsv1.ProtocolHTTP))
	assert.True(t, supports(c, sealsv1.ProtocolHTTPS))
	assert.False(t, supports(c, sealsv1.ProtocolTCP))
}

func TestSupports_EmptyListSupportsNothing(t *testing.T) {
	assert.False(t, supports(class("empty"), sealsv1.ProtocolTCP))
}

func TestCheckUsable_GatewayTypeIsFine(t *testing.T) {
	assert.NoError(t, checkUsable(class("tcp", sealsv1.ProtocolTCP)))
}

func TestCheckUsable_IngressTypeIsRejected(t *testing.T) {
	c := class("legacy", sealsv1.ProtocolHTTP)
	c.Spec.Type = "Ingress"

	err := checkUsable(c)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Ingress")
}

func TestCheckUsable_LoadBalancerTypeIsRejected(t *testing.T) {
	c := class("legacy", sealsv1.ProtocolHTTP)
	c.Spec.Type = "LoadBalancer"

	err := checkUsable(c)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "LoadBalancer")
}

func TestCheckUsable_EmptyTypeIsRejected(t *testing.T) {
	c := class("legacy", sealsv1.ProtocolHTTP)
	c.Spec.Type = ""

	assert.Error(t, checkUsable(c), "an empty type must not be read as Gateway")
}

func TestCheckUsable_NoTLSBlockIsFine(t *testing.T) {
	c := class("https", sealsv1.ProtocolHTTPS)
	c.Spec.TLS = nil

	assert.NoError(t, checkUsable(c))
}

func TestCheckUsable_GatewayTerminatedTLSIsFine(t *testing.T) {
	c := class("https", sealsv1.ProtocolHTTPS)
	c.Spec.TLS = &sealsv1.ExposureTLS{Mode: sealsv1.TLSModeGatewayTerminated}

	assert.NoError(t, checkUsable(c))
}

// Per-exposure certificates are deferred. Falling back silently would issue no
// certificate at all, so the class has to be refused with a message that names
// the mode to use instead.
func TestCheckUsable_PerExposureTLSIsRejected(t *testing.T) {
	c := class("https", sealsv1.ProtocolHTTPS)
	c.Spec.TLS = &sealsv1.ExposureTLS{
		Mode:      sealsv1.TLSModePerExposure,
		IssuerRef: &sealsv1.IssuerRef{Name: "letsencrypt", Kind: "ClusterIssuer"},
	}

	err := checkUsable(c)

	require.Error(t, err)
	assert.Contains(t, err.Error(), string(sealsv1.TLSModePerExposure))
	assert.Contains(t, err.Error(), string(sealsv1.TLSModeGatewayTerminated),
		"the message must name the mode that does work")
}
