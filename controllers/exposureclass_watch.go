package controllers

import (
	"context"

	sigillumv1 "github.com/helmetica-framework/sigillum/api/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// exposureClassIndex is the field index networkConfigsForClass queries. An exposure that
// leaves the class name empty is indexed under "", so a change to a default
// class still finds it.
const exposureClassIndex = "spec.expose.exposureClass"

// exposureClassIndexKeys returns the class names a NetworkConfig references, without
// duplicates.
func exposureClassIndexKeys(o client.Object) []string {
	networkConfig, ok := o.(*sigillumv1.NetworkConfig)
	if !ok {
		return nil
	}

	keys := make([]string, 0, len(networkConfig.Spec.Expose))
	seen := make(map[string]struct{}, len(networkConfig.Spec.Expose))

	for i := range networkConfig.Spec.Expose {
		name := networkConfig.Spec.Expose[i].ExposureClass
		if _, ok := seen[name]; ok {
			continue
		}

		seen[name] = struct{}{}
		keys = append(keys, name)
	}

	return keys
}

// networkConfigsForClass maps a changed ExposureClass to every NetworkConfig it affects: those
// naming it, and, when it is a default, those leaving the class name empty for
// its protocol.
//
// The protocol comes from the class name, not spec.supports. A default class
// that drops its own protocol must still wake the NetworkConfigs using it.
func (r *NetworkConfigManager) networkConfigsForClass(ctx context.Context, obj client.Object) []reconcile.Request {
	keys := map[types.NamespacedName]struct{}{}

	byName := &sigillumv1.NetworkConfigList{}
	if err := r.List(ctx, byName, client.MatchingFields{exposureClassIndex: obj.GetName()}); err != nil {
		r.Log.Error(err, "listing network configs for exposureClass", "name", obj.GetName())
	}

	for i := range byName.Items {
		keys[client.ObjectKeyFromObject(&byName.Items[i])] = struct{}{}
	}

	if p, ok := defaultFor(obj.GetName()); ok {
		byDefault := &sigillumv1.NetworkConfigList{}
		if err := r.List(ctx, byDefault, client.MatchingFields{exposureClassIndex: ""}); err != nil {
			r.Log.Error(err, "listing network configs for default exposureClass", "name", obj.GetName())
		}

		for i := range byDefault.Items {
			if takesDefault(&byDefault.Items[i], p) {
				keys[client.ObjectKeyFromObject(&byDefault.Items[i])] = struct{}{}
			}
		}
	}

	reqs := make([]reconcile.Request, 0, len(keys))
	for key := range keys {
		reqs = append(reqs, reconcile.Request{NamespacedName: key})
	}

	return reqs
}

// defaultFor returns the protocol a class is the default for, if any.
func defaultFor(className string) (sigillumv1.Protocol, bool) {
	for _, p := range []sigillumv1.Protocol{sigillumv1.ProtocolTCP, sigillumv1.ProtocolHTTP, sigillumv1.ProtocolHTTPS} {
		if defaultClassName(p) == className {
			return p, true
		}
	}

	return "", false
}

// takesDefault reports whether a NetworkConfig has an exposure on p with no class name.
func takesDefault(s *sigillumv1.NetworkConfig, p sigillumv1.Protocol) bool {
	for i := range s.Spec.Expose {
		if s.Spec.Expose[i].ExposureClass == "" && s.Spec.Expose[i].Protocol == p {
			return true
		}
	}

	return false
}
