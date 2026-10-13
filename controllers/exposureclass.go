package controllers

import (
	"errors"
	"fmt"
	"strings"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
)

// errClassNotFound separates "the class was deleted" from "the class is wrong".
// A deleted class must leave the objects it already produced alone, so the
// caller has to be able to tell the two apart.
var errClassNotFound = errors.New("exposure class not found")

// defaultClassName is the class an exposure falls back to when it names none:
// its protocol, lowercased. Object names are lowercase DNS labels, the Protocol
// enum is upper case, so the two have to be bridged somewhere.
func defaultClassName(p sealsv1.Protocol) string {
	return strings.ToLower(string(p))
}

// resolveClass picks the ExposureClass an exposure should use.
//
// An empty ExposureClass name means the class named after the protocol, so
// there is only ever one candidate: names are unique and cluster-scoped. That
// is the whole reason there is no default flag.
func resolveClass(classes []sealsv1.ExposureClass, e sealsv1.Exposure) (*sealsv1.ExposureClass, error) {
	className := e.ExposureClass
	if className == "" {
		className = defaultClassName(e.Protocol)
	}

	for i := range classes {
		if classes[i].GetName() != className {
			continue
		}

		if !supports(classes[i], e.Protocol) {
			return nil, fmt.Errorf("exposure class %s does not support %s, it supports %v",
				className, e.Protocol, classes[i].Spec.Supports)
		}

		if err := checkUsable(classes[i]); err != nil {
			return nil, err
		}

		return &classes[i], nil
	}

	return nil, fmt.Errorf("exposure class %s: %w", className, errClassNotFound)
}

// supports reports whether c can carry p.
func supports(c sealsv1.ExposureClass, p sealsv1.Protocol) bool {
	for _, s := range c.Spec.Supports {
		if s == p {
			return true
		}
	}

	return false
}

// checkUsable rejects a class the controller cannot act on yet. The CRD enum
// already blocks most of this on write, but an object written before the enum
// existed still has to fail with a message that says what to change.
func checkUsable(c sealsv1.ExposureClass) error {
	if c.Spec.Type != sealsv1.ExposureTypeGateway {
		return fmt.Errorf("exposure class %s: type %s is not supported, only %s is",
			c.GetName(), c.Spec.Type, sealsv1.ExposureTypeGateway)
	}

	// TLS is nil on every TCP class and on every HTTP class that leaves the
	// certificate to the gateway, so it has to be checked before the mode.
	if c.Spec.TLS != nil && c.Spec.TLS.Mode == sealsv1.TLSModePerExposure {
		return fmt.Errorf("exposure class %s: tls.mode %s is not implemented yet, use %s",
			c.GetName(), sealsv1.TLSModePerExposure, sealsv1.TLSModeGatewayTerminated)
	}

	return nil
}
