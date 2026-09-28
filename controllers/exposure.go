package controllers

import (
	"context"
	"fmt"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	"github.com/helmetica-framework/sigillum/controllers/gatewayapi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// reconcileTCPRoutes drives this Seal's TCPRoutes to match exposures: one route
// per exposure, and nothing left over.
//
// Deleting the leftovers is what makes a rename work. A renamed exposure is a
// new name, so it gets a new route and the old one has to go, or it keeps
// pointing at a listener that no longer exists.
func (r *SealManager) reconcileTCPRoutes(
	ctx context.Context,
	seal *sealsv1.Seal,
	exposures []sealsv1.Exposure,
) error {
	owner, err := controllerRef(seal, r.Scheme)
	if err != nil {
		return fmt.Errorf("building owner reference: %w", err)
	}

	// Build every route before applying any, so a bad exposure cannot leave
	// half the routes applied.
	var routes []*unstructured.Unstructured

	for _, e := range exposures {
		if e.Protocol != sealsv1.ProtocolTCP {
			continue
		}

		backend := gatewayapi.Backend{Service: e.Backend.Service, Port: e.Backend.Port}

		route, err := gatewayapi.BuildTCPRoute(seal.GetName(), seal.GetNamespace(), e.Name, backend, owner)
		if err != nil {
			return fmt.Errorf("building tcproute: %w", err)
		}

		routes = append(routes, route)
	}

	applied := map[string]bool{}

	for _, route := range routes {
		err = r.Apply(ctx, client.ApplyConfigurationFromUnstructured(route), fieldOwner, client.ForceOwnership)
		if err != nil {
			return fmt.Errorf("applying tcproute: %w", err)
		}

		applied[route.GetName()] = true
	}

	routeList := gatewayapi.NewList(gatewayapi.TCPRouteGVK)

	err = r.List(ctx, routeList, &client.ListOptions{Namespace: seal.GetNamespace()})
	if err != nil {
		return fmt.Errorf("listing tcproutes: %w", err)
	}

	for _, route := range routeList.Items {
		if !metav1.IsControlledBy(&route, seal) {
			continue
		}

		if applied[route.GetName()] {
			continue
		}

		if err := r.Delete(ctx, &route); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting tcproute %s: %w", route.GetName(), err)
		}
	}

	return nil
}
