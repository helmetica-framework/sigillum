package controllers

import (
	"context"
	"fmt"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	"github.com/helmetica-framework/sigillum/controllers/gatewayapi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// reconcileListenerSet drives this Seal's one ListenerSet to match listeners.
//
// A Seal with no TCP exposures gets no ListenerSet at all. spec.listeners
// requires at least one entry, so an empty list means delete the object rather
// than apply an invalid one.
func (r *SealManager) reconcileListenerSet(
	ctx context.Context,
	seal *sealsv1.Seal,
	parent gatewayapi.ShardRef,
	listeners []gatewayapi.Listener,
) error {
	owner, err := controllerRef(seal, r.Scheme)
	if err != nil {
		return fmt.Errorf("building owner reference: %w", err)
	}

	ls, err := gatewayapi.BuildListenerSet(seal.GetName(), seal.GetNamespace(), parent, listeners, owner)
	if err != nil {
		return fmt.Errorf("building listenerset: %w", err)
	}

	if ls == nil {
		target := gatewayapi.New(gatewayapi.ListenerSetGVK)
		target.SetName(seal.GetName())
		target.SetNamespace(seal.GetNamespace())

		err := r.Delete(ctx, target)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting listenerset: %w", err)
		}

		return nil
	}

	err = r.Apply(ctx, client.ApplyConfigurationFromUnstructured(ls), fieldOwner, client.ForceOwnership)
	if err != nil {
		return fmt.Errorf("applying listenerset: %w", err)
	}

	return nil
}
