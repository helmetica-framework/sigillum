package controllers

import (
	"context"
	"errors"
	"fmt"
	"slices"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	"github.com/helmetica-framework/sigillum/controllers/gatewayapi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// exposureOutcome is one exposure's published status. Failed separates
// "broken" from "waiting on the gateway controller", which both have
// Ready false.
type exposureOutcome struct {
	Status sealsv1.ExposureStatus
	Failed bool
}

// exposureResult is what reconciling one exposure produced.
type exposureResult struct {
	Shard         string
	Address       string
	Port          int32
	Hostname      string
	RouteReady    bool
	RouteMsg      string
	ConflictCount int32
	Err           error
}

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

// buildExposureStatus turns one exposure's result into its published status.
//
// A missing gateway address gives no URL at all: "tcp://:30007" looks like a
// working address and is not one.
func buildExposureStatus(e sealsv1.Exposure, class string, res exposureResult) sealsv1.ExposureStatus {
	st := sealsv1.ExposureStatus{
		Name:          e.Name,
		Class:         class,
		Shard:         res.Shard,
		Port:          res.Port,
		ConflictCount: res.ConflictCount,
	}

	if res.Address != "" {
		st.Address = res.Address
		st.URL = fmt.Sprintf("tcp://%s:%d", res.Address, res.Port)
	}

	switch {
	case res.Err != nil:
		st.Message = res.Err.Error()
	case e.Protocol != sealsv1.ProtocolTCP:
		st.Message = fmt.Sprintf("protocol %s is not supported yet", e.Protocol)
	case res.Address == "":
		st.Message = "waiting for gateway address"
	case !res.RouteReady:
		st.Message = res.RouteMsg
	default:
		st.Ready = true
	}

	return st
}

// rollUp sets the Seal phase from its exposures. done is the phase to report
// when every exposure is ready. A failed exposure beats a failed done, and a
// waiting exposure beats only a ready one.
func rollUp(outcomes []exposureOutcome, done phase) phase {
	if len(outcomes) == 0 {
		return done
	}

	done.Exposures = make([]sealsv1.ExposureStatus, len(outcomes))
	for i, o := range outcomes {
		done.Exposures[i] = o.Status
	}

	for _, o := range outcomes {
		if o.Failed {
			done.Phase = sealsv1.SealPhaseFailed
			done.Message = fmt.Sprintf("exposure %q: %s", o.Status.Name, o.Status.Message)
			return done
		}
	}

	if done.Phase != sealsv1.SealPhaseReady {
		return done
	}

	for _, o := range outcomes {
		if !o.Status.Ready {
			done.Phase = sealsv1.SealPhasePending
			done.Message = fmt.Sprintf("exposure %q: %s", o.Status.Name, o.Status.Message)
			return done
		}
	}

	return done
}

// reconcileExposures drives the ListenerSet and TCPRoutes for seal's
// exposures and reports one outcome per exposure, in spec order.
//
// The error is for problems a retry might fix. Everything else is only
// reported in the returned statuses.
func (r *SealManager) reconcileExposures(
	ctx context.Context,
	seal *sealsv1.Seal,
	classes []sealsv1.ExposureClass,
) ([]exposureOutcome, error) {
	const resolving = "resolving exposure class"

	outcomes := make([]exposureOutcome, len(seal.Spec.Expose))
	for i, e := range seal.Spec.Expose {
		outcomes[i] = exposureOutcome{
			Status: sealsv1.ExposureStatus{
				Name:    e.Name,
				Message: resolving,
			},
		}
	}

	prev := map[string]sealsv1.ExposureStatus{}
	for _, st := range seal.Status.Exposures {
		prev[st.Name] = st
	}

	// fail keeps the last published status for exposures this pass did not get
	// to, so a short error does not wipe their port, URL or conflict count.
	fail := func(err error) ([]exposureOutcome, error) {
		for i := range outcomes {
			if outcomes[i].Status.Message != resolving {
				continue
			}
			if st, ok := prev[outcomes[i].Status.Name]; ok {
				outcomes[i].Status = st
			}
		}
		return outcomes, err
	}

	// classOf stays nil for every exposure that does not go on to placement.
	// keepAsIs marks exposures whose class is gone or whose port gave up on
	// conflicts: their listener and route must survive untouched.
	classOf := make([]*sealsv1.ExposureClass, len(seal.Spec.Expose))
	keepAsIs := make([]bool, len(seal.Spec.Expose))

	for i, e := range seal.Spec.Expose {
		name := e.ExposureClass
		if name == "" {
			name = defaultClassName(e.Protocol)
		}

		if e.Protocol != sealsv1.ProtocolTCP {
			outcomes[i] = exposureOutcome{
				Status: buildExposureStatus(e, name, exposureResult{}),
				Failed: true,
			}
			continue
		}

		class, err := resolveClass(classes, e)
		switch {
		case errors.Is(err, errClassNotFound):
			keepAsIs[i] = true
			outcomes[i].Status.Class = name
			outcomes[i].Status.Message = fmt.Sprintf("exposure class %q no longer exists", name)
			outcomes[i].Failed = true
		case err != nil:
			outcomes[i] = exposureOutcome{
				Status: buildExposureStatus(e, name, exposureResult{Err: err}),
				Failed: true,
			}
		default:
			classOf[i] = class
		}
	}

	own, err := readOwnListenerSet(ctx, r.Client, seal)
	if err != nil {
		return fail(err)
	}

	// A conflict in the cache can be old. Check it live before moving a port.
	states := gatewayapi.ListenerStates(own)
	for _, s := range states {
		if s == gatewayapi.ListenerConflicted {
			own, err = readOwnListenerSet(ctx, r.APIReader, seal)
			if err != nil {
				return fail(err)
			}
			states = gatewayapi.ListenerStates(own)
			break
		}
	}

	ports := listenerPorts(own)

	mustMove := make([]bool, len(seal.Spec.Expose))
	counts := make([]int32, len(seal.Spec.Expose))
	conflicted := map[string]struct{}{}

	for i, e := range seal.Spec.Expose {
		if classOf[i] == nil {
			continue
		}

		port, has := ports[e.Name]
		if !has {
			continue // new exposure, placement gives it a port
		}

		action, count := stepConflict(prev[e.Name].ConflictCount, states[e.Name])
		counts[i] = count

		switch action {
		case movePort:
			mustMove[i] = true
			conflicted[e.Name] = struct{}{}
		case giveUp:
			shard, _ := gatewayapi.ParentShard(own)
			outcomes[i] = exposureOutcome{
				Status: buildExposureStatus(e, classOf[i].GetName(), exposureResult{
					Shard:         shard.Name,
					Port:          port,
					ConflictCount: count,
					Err:           errors.New(conflictMessage(e.Name, port, shard)),
				}),
				Failed: true,
			}
			keepAsIs[i] = true
			classOf[i] = nil
		}
	}

	// One ListenerSet per Seal means one shard per Seal. The first exposure
	// with a class picks it.
	first := -1
	for i := range classOf {
		if classOf[i] != nil {
			first = i
			break
		}
	}

	var (
		shard  gatewayapi.ShardRef
		sticky bool
		sets   []unstructured.Unstructured
	)
	if first >= 0 {
		shards, err := r.listShards(ctx, classOf[first])
		if err != nil {
			return fail(err)
		}

		_, sticky = stickyShard(own, shards)

		// Read live only when a port must be picked.
		needPort := !sticky
		for i, e := range seal.Spec.Expose {
			if _, has := ports[e.Name]; classOf[i] != nil && (!has || mustMove[i]) {
				needPort = true
			}
		}
		if needPort {
			sets, err = r.listAllListenerSets(ctx, true)
			if err != nil {
				return fail(err)
			}

			// The cache may not have seen the ListenerSet written a moment ago.
			own = findOwn(sets, seal)
			ports = listenerPorts(own)
		}

		shard, sticky = stickyShard(own, shards)

		if !sticky {
			shard, err = placeExposure(shards, shardCounts(sets), classOf[first])
			if err != nil {
				err = fmt.Errorf("exposure %q: %w", seal.Spec.Expose[first].Name, err)
				for i, e := range seal.Spec.Expose {
					if classOf[i] != nil {
						outcomes[i] = exposureOutcome{
							Status: buildExposureStatus(e, classOf[i].GetName(), exposureResult{Err: err}),
							Failed: true,
						}
					}
				}
				return outcomes, err
			}
		}

		for i, e := range seal.Spec.Expose {
			if classOf[i] == nil || classOf[i].GetName() == classOf[first].GetName() {
				continue
			}

			match, err := r.listShards(ctx, classOf[i])
			if err != nil {
				return fail(err)
			}
			if slices.Contains(match, shard) {
				continue
			}

			err = fmt.Errorf(
				"exposure %q: class %q does not match shard %s/%s, which exposure %q uses: all TCP exposures of one Seal share one shard",
				e.Name, classOf[i].GetName(), shard.Namespace, shard.Name, seal.Spec.Expose[first].Name)
			outcomes[i] = exposureOutcome{
				Status: buildExposureStatus(e, classOf[i].GetName(), exposureResult{Err: err}),
				Failed: true,
			}
			classOf[i] = nil
		}
	}

	taken := takenPortsWithConflicts(sets, shard, own, conflicted)

	// The Seal's own ports are not in taken, but the ports that stay are still
	// in use by this ListenerSet.
	for i, e := range seal.Spec.Expose {
		port, has := ports[e.Name]
		if !has {
			continue
		}
		if keepAsIs[i] || (classOf[i] != nil && sticky && !mustMove[i]) {
			taken[port] = struct{}{}
		}
	}
	res := make([]exposureResult, len(seal.Spec.Expose))
	var listeners []gatewayapi.Listener

	for i, e := range seal.Spec.Expose {
		port, has := ports[e.Name]

		if keepAsIs[i] {
			if has {
				listeners = append(listeners, gatewayapi.Listener{Name: e.Name, Protocol: "TCP", Port: port})
			}
			continue
		}
		if classOf[i] == nil {
			continue
		}

		// A new shard means new ports: the old ones belong to the old shard.
		if !has || mustMove[i] || !sticky {
			var err error
			if classOf[i].Spec.PortRange == nil {
				err = fmt.Errorf("exposure class %q has no portRange", classOf[i].GetName())
			} else {
				port, err = allocatePort(*classOf[i].Spec.PortRange, taken, r.random())
			}
			if err != nil {
				outcomes[i] = exposureOutcome{
					Status: buildExposureStatus(e, classOf[i].GetName(), exposureResult{Err: err}),
					Failed: true,
				}
				classOf[i] = nil
				continue
			}
			taken[port] = struct{}{}
		}

		listeners = append(listeners, gatewayapi.Listener{Name: e.Name, Protocol: "TCP", Port: port})
		res[i] = exposureResult{Shard: shard.Name, Port: port, ConflictCount: counts[i]}
	}

	parent := shard
	if first < 0 {
		parent, _ = gatewayapi.ParentShard(own) // only kept listeners are left
	}
	if err := r.reconcileListenerSet(ctx, seal, parent, listeners); err != nil {
		return fail(err)
	}

	var routed []sealsv1.Exposure
	for i, e := range seal.Spec.Expose {
		_, has := ports[e.Name]
		if classOf[i] != nil || (keepAsIs[i] && has) {
			routed = append(routed, e)
		}
	}
	if err := r.reconcileTCPRoutes(ctx, seal, routed); err != nil {
		return fail(err)
	}

	addr := ""
	if first >= 0 {
		gw := gatewayapi.New(gatewayapi.GatewayGVK)
		err := r.Get(ctx, client.ObjectKey{Namespace: shard.Namespace, Name: shard.Name}, gw)
		if err != nil && !apierrors.IsNotFound(err) {
			return fail(fmt.Errorf("getting gateway: %w", err))
		}
		addr, _ = gatewayapi.GatewayAddress(gw)
	}

	for i, e := range seal.Spec.Expose {
		if classOf[i] == nil {
			continue
		}

		route := gatewayapi.New(gatewayapi.TCPRouteGVK)
		key := client.ObjectKey{Namespace: seal.GetNamespace(), Name: gatewayapi.RouteName(seal.GetName(), e.Name)}
		err := r.Get(ctx, key, route)
		if apierrors.IsNotFound(err) {
			route = nil
		} else if err != nil {
			return fail(fmt.Errorf("getting tcproute: %w", err))
		}

		res[i].Address = addr
		res[i].RouteReady, res[i].RouteMsg = gatewayapi.RouteIsReady(route)
		outcomes[i] = exposureOutcome{Status: buildExposureStatus(e, classOf[i].GetName(), res[i])}
	}

	return outcomes, nil
}

// findOwn picks the Seal's own ListenerSet out of sets. Nil means none.
func findOwn(sets []unstructured.Unstructured, seal *sealsv1.Seal) *unstructured.Unstructured {
	for i := range sets {
		if sets[i].GetName() == seal.GetName() && sets[i].GetNamespace() == seal.GetNamespace() {
			return &sets[i]
		}
	}
	return nil
}

func listenerPorts(own *unstructured.Unstructured) map[string]int32 {
	ports := map[string]int32{}
	for _, l := range gatewayapi.Listeners(own) {
		ports[l.Name] = l.Port
	}
	return ports
}

// readOwnListenerSet reads the Seal's own ListenerSet. Nil means there is none yet.
func readOwnListenerSet(ctx context.Context, reader client.Reader, seal *sealsv1.Seal) (*unstructured.Unstructured, error) {
	obj := gatewayapi.New(gatewayapi.ListenerSetGVK)

	err := reader.Get(ctx, client.ObjectKeyFromObject(seal), obj)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting own listenerset: %w", err)
	}

	return obj, nil
}
