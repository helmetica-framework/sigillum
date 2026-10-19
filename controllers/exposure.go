package controllers

import (
	"context"
	"errors"
	"fmt"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	"github.com/helmetica-framework/sigillum/controllers/gatewayapi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwac "sigs.k8s.io/gateway-api/applyconfiguration/apis/v1"
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
	var routes []*gwac.TCPRouteApplyConfiguration

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
		err = r.Apply(ctx, route, fieldOwner, client.ForceOwnership)
		if err != nil {
			return fmt.Errorf("applying tcproute: %w", err)
		}

		applied[*route.Name] = true
	}

	routeList := &gwv1.TCPRouteList{}

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

// reconcileExposures drives the ListenerSets and TCPRoutes for seal's
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

	own, err := readOwnedSets(ctx, r.Client, seal)
	if err != nil {
		return fail(err)
	}

	// A conflict in the cache can be old. Check it live before moving a port.
	if hasConflict(own) {
		own, err = readOwnedSets(ctx, r.APIReader, seal)
		if err != nil {
			return fail(err)
		}
	}

	mustMove := make([]bool, len(seal.Spec.Expose))
	counts := make([]int32, len(seal.Spec.Expose))

	for i, e := range seal.Spec.Expose {
		if classOf[i] == nil {
			continue
		}

		set := exposureSet(own, seal, e.Name)
		port, has := listenerPort(set, e.Name)
		if !has {
			continue // new exposure, placement gives it a port
		}

		action, count := stepConflict(prev[e.Name].ConflictCount, gatewayapi.ListenerStates(set)[e.Name])
		counts[i] = count

		switch action {
		case movePort:
			mustMove[i] = true
		case giveUp:
			shard, _ := gatewayapi.ParentShard(set)
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

	shardsOf := map[string][]gatewayapi.ShardRef{}
	needPort := false

	for i, e := range seal.Spec.Expose {
		if classOf[i] == nil {
			continue
		}

		name := classOf[i].GetName()
		if _, done := shardsOf[name]; !done {
			shards, err := r.listShards(ctx, classOf[i])
			if err != nil {
				return fail(err)
			}
			shardsOf[name] = shards
		}

		set := exposureSet(own, seal, e.Name)
		_, has := listenerPort(set, e.Name)
		_, sticky := stickyShard(set, shardsOf[name])
		if !has || mustMove[i] || !sticky {
			needPort = true
		}
	}

	// Read live only when a port must be picked.
	var sets []gwv1.ListenerSet
	if needPort {
		sets, err = r.listAllListenerSets(ctx, true)
		if err != nil {
			return fail(err)
		}

		// The cache may not have seen the sets written a moment ago.
		own = ownedSets(sets, seal)
	}

	p := newPlacer(sets)
	res := make([]exposureResult, len(seal.Spec.Expose))
	placed := make([]gatewayapi.ShardRef, len(seal.Spec.Expose))
	var (
		wants    []wantSet
		placeErr error
	)

	failOne := func(i int, err error) {
		outcomes[i] = exposureOutcome{
			Status: buildExposureStatus(seal.Spec.Expose[i], classOf[i].GetName(), exposureResult{Err: err}),
			Failed: true,
		}
		classOf[i] = nil
	}

	for i, e := range seal.Spec.Expose {
		class := classOf[i]
		if class == nil {
			continue
		}

		set := exposureSet(own, seal, e.Name)
		shards := shardsOf[class.GetName()]

		shard, sticky := stickyShard(set, shards)
		if !sticky {
			shard, err = placeExposure(shards, p.counts, class)
			if err != nil {
				err = fmt.Errorf("exposure %q: %w", e.Name, err)
				if placeErr == nil {
					placeErr = err
				}
				failOne(i, err)
				continue
			}
		}

		// A new shard means a new port: the old one belongs to the old shard.
		port, has := listenerPort(set, e.Name)
		if !has || mustMove[i] || !sticky {
			var conflicted map[string]struct{}
			if mustMove[i] {
				conflicted = map[string]struct{}{e.Name: {}}
			}

			if class.Spec.PortRange == nil {
				err = fmt.Errorf("exposure class %q has no portRange", class.GetName())
			} else {
				port, err = allocatePort(*class.Spec.PortRange, p.takenOn(shard, set, conflicted), r.random())
			}
			if err != nil {
				failOne(i, err)
				continue
			}
		}

		p.claim(shard, port, !sticky)
		wants = append(wants, wantSet{Exposure: e.Name, Parent: shard, Port: port})
		placed[i] = shard
		res[i] = exposureResult{Shard: shard.Name, Port: port, ConflictCount: counts[i]}
	}

	var (
		keep   []string
		routed []sealsv1.Exposure
	)
	for i, e := range seal.Spec.Expose {
		_, has := listenerPort(exposureSet(own, seal, e.Name), e.Name)
		if keepAsIs[i] && has {
			keep = append(keep, e.Name)
		}
		if classOf[i] != nil || (keepAsIs[i] && has) {
			routed = append(routed, e)
		}
	}

	if err := r.reconcileListenerSets(ctx, seal, wants, keep); err != nil {
		return fail(err)
	}
	if err := r.reconcileTCPRoutes(ctx, seal, routed); err != nil {
		return fail(err)
	}

	addrOf := map[gatewayapi.ShardRef]string{}
	for _, w := range wants {
		if _, done := addrOf[w.Parent]; done {
			continue
		}

		gw := &gwv1.Gateway{}
		err := r.Get(ctx, client.ObjectKey{Namespace: w.Parent.Namespace, Name: w.Parent.Name}, gw)
		if err != nil && !apierrors.IsNotFound(err) {
			return fail(fmt.Errorf("getting gateway: %w", err))
		}
		addrOf[w.Parent], _ = gatewayapi.GatewayAddress(gw)
	}

	for i, e := range seal.Spec.Expose {
		if classOf[i] == nil {
			continue
		}

		route := &gwv1.TCPRoute{}
		key := client.ObjectKey{Namespace: seal.GetNamespace(), Name: gatewayapi.RouteName(seal.GetName(), e.Name)}
		err := r.Get(ctx, key, route)
		if apierrors.IsNotFound(err) {
			route = nil
		} else if err != nil {
			return fail(fmt.Errorf("getting tcproute: %w", err))
		}

		res[i].Address = addrOf[placed[i]]
		res[i].RouteReady, res[i].RouteMsg = gatewayapi.RouteIsReady(route)
		outcomes[i] = exposureOutcome{Status: buildExposureStatus(e, classOf[i].GetName(), res[i])}
	}

	// Returned last, so the siblings of an exposure that found no room are
	// still written.
	return outcomes, placeErr
}

// placer tracks shard use while one pass places exposures one by one. Without
// it, two exposures placed in the same pass would see the same free slot and
// the same free port.
type placer struct {
	sets    []gwv1.ListenerSet
	counts  map[gatewayapi.ShardRef]int
	claimed map[gatewayapi.ShardRef]map[int32]struct{}
}

func newPlacer(sets []gwv1.ListenerSet) *placer {
	return &placer{
		sets:    sets,
		counts:  shardCounts(sets),
		claimed: map[gatewayapi.ShardRef]map[int32]struct{}{},
	}
}

// takenOn is the set of ports in use on shard: every set's ports except own's
// (unless conflicted), plus the ports claimed in this pass.
func (p *placer) takenOn(shard gatewayapi.ShardRef, own *gwv1.ListenerSet, conflicted map[string]struct{}) map[int32]struct{} {
	taken := takenPortsWithConflicts(p.sets, shard, own, conflicted)

	for port := range p.claimed[shard] {
		taken[port] = struct{}{}
	}

	return taken
}

// claim records port on shard. moved is true when the exposure was not on
// shard before, so shard has one more listener.
func (p *placer) claim(shard gatewayapi.ShardRef, port int32, moved bool) {
	if p.claimed[shard] == nil {
		p.claimed[shard] = map[int32]struct{}{}
	}
	p.claimed[shard][port] = struct{}{}

	if moved {
		p.counts[shard]++
	}
}

// wantSet is one exposure's ListenerSet: one listener for Exposure on Port,
// parented to Parent.
type wantSet struct {
	Exposure string
	Parent   gatewayapi.ShardRef
	Port     int32
}

// reconcileListenerSets drives the Seal's ListenerSets to match want: one set
// per exposure, and nothing left over. The sets of exposures named in keep
// are left as they are.
func (r *SealManager) reconcileListenerSets(
	ctx context.Context,
	seal *sealsv1.Seal,
	want []wantSet,
	keep []string,
) error {
	owner, err := controllerRef(seal, r.Scheme)
	if err != nil {
		return fmt.Errorf("building owner reference: %w", err)
	}

	// Build every set before applying any, so a bad port cannot leave half
	// the sets applied.
	sets := make([]*gwac.ListenerSetApplyConfiguration, 0, len(want))

	for _, w := range want {
		set, err := gatewayapi.BuildListenerSet(
			gatewayapi.ListenerSetName(seal.GetName(), w.Exposure),
			seal.GetNamespace(),
			w.Parent,
			[]gatewayapi.Listener{{Name: w.Exposure, Protocol: "TCP", Port: w.Port}},
			owner,
		)
		if err != nil {
			return fmt.Errorf("building listenerset: %w", err)
		}

		sets = append(sets, set)
	}

	applied := map[string]bool{}

	for _, set := range sets {
		err = r.Apply(ctx, set, fieldOwner, client.ForceOwnership)
		if err != nil {
			return fmt.Errorf("applying listenerset %s: %w", *set.Name, err)
		}

		applied[*set.Name] = true
	}

	kept := make(map[string]bool, len(keep))
	for _, name := range keep {
		kept[gatewayapi.ListenerSetName(seal.GetName(), name)] = true
	}

	owned, err := readOwnedSets(ctx, r.Client, seal)
	if err != nil {
		return err
	}

	for name, set := range owned {
		if applied[name] || kept[name] {
			continue
		}

		if err := r.Delete(ctx, set); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting listenerset %s: %w", name, err)
		}
	}

	return nil
}

// ownedSets picks the sets seal controls out of sets, keyed by name.
func ownedSets(sets []gwv1.ListenerSet, seal *sealsv1.Seal) map[string]*gwv1.ListenerSet {
	owned := map[string]*gwv1.ListenerSet{}

	for i := range sets {
		if metav1.IsControlledBy(&sets[i], seal) {
			owned[sets[i].GetName()] = &sets[i]
		}
	}

	return owned
}

// readOwnedSets lists the sets in the Seal's namespace and keeps the ones the
// Seal controls, keyed by name.
func readOwnedSets(ctx context.Context, reader client.Reader, seal *sealsv1.Seal) (map[string]*gwv1.ListenerSet, error) {
	list := &gwv1.ListenerSetList{}

	err := reader.List(ctx, list, client.InNamespace(seal.GetNamespace()))
	if err != nil {
		return nil, fmt.Errorf("listing own listenersets: %w", err)
	}

	return ownedSets(list.Items, seal), nil
}

// exposureSet picks the set of exposure out of own. Nil means none.
func exposureSet(own map[string]*gwv1.ListenerSet, seal *sealsv1.Seal, exposure string) *gwv1.ListenerSet {
	return own[gatewayapi.ListenerSetName(seal.GetName(), exposure)]
}

// hasConflict reports whether any listener on sets is Conflicted.
func hasConflict(sets map[string]*gwv1.ListenerSet) bool {
	for _, set := range sets {
		for _, state := range gatewayapi.ListenerStates(set) {
			if state == gatewayapi.ListenerConflicted {
				return true
			}
		}
	}

	return false
}

// listenerPort is the port of the listener named exposure on set.
func listenerPort(set *gwv1.ListenerSet, exposure string) (int32, bool) {
	for _, l := range gatewayapi.Listeners(set) {
		if l.Name == exposure {
			return l.Port, true
		}
	}

	return 0, false
}
