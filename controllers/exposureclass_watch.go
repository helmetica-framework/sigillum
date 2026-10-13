package controllers

import (
	"context"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// exposureClassIndex is the field index sealsForClass queries. An exposure that
// leaves the class name empty is indexed under "", so a change to a default
// class still finds it.
const exposureClassIndex = "spec.expose.exposureClass"

// exposureClassIndexKeys returns the class names a Seal references, without
// duplicates.
func exposureClassIndexKeys(o client.Object) []string {
	seal, ok := o.(*sealsv1.Seal)
	if !ok {
		return nil
	}

	keys := make([]string, 0, len(seal.Spec.Expose))
	seen := make(map[string]struct{}, len(seal.Spec.Expose))

	for i := range seal.Spec.Expose {
		name := seal.Spec.Expose[i].ExposureClass
		if _, ok := seen[name]; ok {
			continue
		}

		seen[name] = struct{}{}
		keys = append(keys, name)
	}

	return keys
}

// sealsForClass maps a changed ExposureClass to every Seal it affects: those
// naming it, and, when it is a default, those leaving the class name empty for
// its protocol.
//
// The protocol comes from the class name, not spec.supports. A default class
// that drops its own protocol must still wake the Seals using it.
func (r *SealManager) sealsForClass(ctx context.Context, obj client.Object) []reconcile.Request {
	keys := map[types.NamespacedName]struct{}{}

	byName := &sealsv1.SealList{}
	if err := r.List(ctx, byName, client.MatchingFields{exposureClassIndex: obj.GetName()}); err != nil {
		r.Log.Error(err, "listing seals for exposureClass", "name", obj.GetName())
	}

	for i := range byName.Items {
		keys[client.ObjectKeyFromObject(&byName.Items[i])] = struct{}{}
	}

	if p, ok := defaultFor(obj.GetName()); ok {
		byDefault := &sealsv1.SealList{}
		if err := r.List(ctx, byDefault, client.MatchingFields{exposureClassIndex: ""}); err != nil {
			r.Log.Error(err, "listing seals for default exposureClass", "name", obj.GetName())
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
func defaultFor(className string) (sealsv1.Protocol, bool) {
	for _, p := range []sealsv1.Protocol{sealsv1.ProtocolTCP, sealsv1.ProtocolHTTP, sealsv1.ProtocolHTTPS} {
		if defaultClassName(p) == className {
			return p, true
		}
	}

	return "", false
}

// takesDefault reports whether a Seal has an exposure on p with no class name.
func takesDefault(s *sealsv1.Seal, p sealsv1.Protocol) bool {
	for i := range s.Spec.Expose {
		if s.Spec.Expose[i].ExposureClass == "" && s.Spec.Expose[i].Protocol == p {
			return true
		}
	}

	return false
}
