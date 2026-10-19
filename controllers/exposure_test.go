package controllers

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	"github.com/helmetica-framework/sigillum/controllers/gatewayapi"
)

// tcpExposure is one TCP exposure pointing at the redis service.
func tcpExposure(name string, port int32) sealsv1.Exposure {
	return sealsv1.Exposure{
		Name:     name,
		Protocol: sealsv1.ProtocolTCP,
		Backend:  sealsv1.Backend{Service: "redis", Port: port},
	}
}

// tcpRoute seeds a TCPRoute owned by the Seal carrying uid.
func tcpRoute(name, namespace string, uid types.UID) gwv1.TCPRoute {
	return gwv1.TCPRoute{ObjectMeta: metav1.ObjectMeta{
		Name:      name,
		Namespace: namespace,
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion:         sealsv1.GroupVersion.String(),
			Kind:               "Seal",
			Name:               "whoever",
			UID:                uid,
			Controller:         ptr.To(true),
			BlockOwnerDeletion: ptr.To(true),
		}},
	}}
}

// routeNames lists every TCPRoute name in namespace, sorted.
func routeNames(t *testing.T, c client.Client, namespace string) []string {
	t.Helper()

	list := &gwv1.TCPRouteList{}
	require.NoError(t, c.List(context.Background(), list, client.InNamespace(namespace)))

	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].GetName())
	}
	sort.Strings(names)

	return names
}

// readTCPRoute fetches one TCPRoute by name. The second result is false when
// there is none.
func readTCPRoute(t *testing.T, c client.Client, name, namespace string) (*gwv1.TCPRoute, bool) {
	t.Helper()

	obj := &gwv1.TCPRoute{}
	err := c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: namespace}, obj)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	require.NoError(t, err)

	return obj, true
}

func TestReconcileTCPRoutes_AppliesOneRoutePerExposure(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileTCPRoutes(context.Background(), s,
		[]sealsv1.Exposure{tcpExposure("direct", 6379), tcpExposure("admin", 9121)})
	require.NoError(t, err)

	assert.Equal(t, []string{"sample-admin", "sample-direct"}, routeNames(t, c, "default"))
}

// The route has to reach the exposure's own backend, or traffic lands on the
// wrong service.
func TestReconcileTCPRoutes_CarriesTheExposureBackend(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileTCPRoutes(context.Background(), s,
		[]sealsv1.Exposure{tcpExposure("direct", 6379)})
	require.NoError(t, err)

	obj, found := readTCPRoute(t, c, "sample-direct", "default")
	require.True(t, found)

	require.Len(t, obj.Spec.Rules, 1)
	require.Len(t, obj.Spec.Rules[0].BackendRefs, 1)

	b := obj.Spec.Rules[0].BackendRefs[0]
	assert.EqualValues(t, "redis", b.Name)
	assert.EqualValues(t, 6379, *b.Port)
}

// The section name is what binds the route to one listener. Without it the
// route would answer on every port the shard serves.
func TestReconcileTCPRoutes_BindsToTheExposuresListener(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileTCPRoutes(context.Background(), s,
		[]sealsv1.Exposure{tcpExposure("direct", 6379)})
	require.NoError(t, err)

	obj, found := readTCPRoute(t, c, "sample-direct", "default")
	require.True(t, found)

	require.Len(t, obj.Spec.ParentRefs, 1)

	p := obj.Spec.ParentRefs[0]
	assert.EqualValues(t, "ListenerSet", *p.Kind)
	assert.EqualValues(t, "sample-direct", p.Name)
	assert.EqualValues(t, "direct", *p.SectionName)
}

func TestReconcileTCPRoutes_SetsTheControllerOwnerReference(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileTCPRoutes(context.Background(), s,
		[]sealsv1.Exposure{tcpExposure("direct", 6379)})
	require.NoError(t, err)

	obj, found := readTCPRoute(t, c, "sample-direct", "default")
	require.True(t, found)

	refs := obj.GetOwnerReferences()

	require.Len(t, refs, 1)
	assert.Equal(t, "Seal", refs[0].Kind)
	assert.Equal(t, "sample", refs[0].Name)
	assert.Equal(t, sealUID, refs[0].UID)
	assert.True(t, *refs[0].Controller)
	assert.True(t, *refs[0].BlockOwnerDeletion)
}

// A dropped exposure leaves a route pointing at a listener that no longer
// exists, so the leftover has to go.
func TestReconcileTCPRoutes_DeletesTheRouteOfADroppedExposure(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)
	ctx := context.Background()

	require.NoError(t, m.reconcileTCPRoutes(ctx, s,
		[]sealsv1.Exposure{tcpExposure("direct", 6379), tcpExposure("admin", 9121)}))

	require.NoError(t, m.reconcileTCPRoutes(ctx, s,
		[]sealsv1.Exposure{tcpExposure("direct", 6379)}))

	assert.Equal(t, []string{"sample-direct"}, routeNames(t, c, "default"))
}

// A rename is a new exposure with a new port, so the old route must not stay
// behind serving the old name.
func TestReconcileTCPRoutes_ARenameReplacesTheRoute(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)
	ctx := context.Background()

	require.NoError(t, m.reconcileTCPRoutes(ctx, s,
		[]sealsv1.Exposure{tcpExposure("direct", 6379)}))

	require.NoError(t, m.reconcileTCPRoutes(ctx, s,
		[]sealsv1.Exposure{tcpExposure("primary", 6379)}))

	assert.Equal(t, []string{"sample-primary"}, routeNames(t, c, "default"))
}

func TestReconcileTCPRoutes_NoExposuresRemovesEveryRoute(t *testing.T) {
	s := seal(1)
	existing := tcpRoute("sample-direct", "default", sealUID)
	m, c := newManager(s, &existing)

	err := m.reconcileTCPRoutes(context.Background(), s, nil)

	require.NoError(t, err)
	assert.Empty(t, routeNames(t, c, "default"))
}

func TestReconcileTCPRoutes_NoExposuresAndNoRoutesIsNotAnError(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileTCPRoutes(context.Background(), s, nil)

	require.NoError(t, err, "a Seal that never had a TCP exposure reconciles cleanly")
	assert.Empty(t, routeNames(t, c, "default"))
}

// Two Seals share a namespace only by accident of naming, but one Seal deleting
// another's route would take a live service down.
func TestReconcileTCPRoutes_LeavesAnotherSealsRouteAlone(t *testing.T) {
	s := seal(1)
	foreign := tcpRoute("sample-other", "default", types.UID("99999999-9999-9999-9999-999999999999"))
	m, c := newManager(s, &foreign)

	err := m.reconcileTCPRoutes(context.Background(), s,
		[]sealsv1.Exposure{tcpExposure("direct", 6379)})
	require.NoError(t, err)

	assert.Equal(t, []string{"sample-direct", "sample-other"}, routeNames(t, c, "default"),
		"pruning matches the owner UID, not the name prefix")
}

// A route with no owner at all was written by hand. Deleting it is not this
// controller's business.
func TestReconcileTCPRoutes_LeavesAnUnownedRouteAlone(t *testing.T) {
	s := seal(1)
	orphan := gwv1.TCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "handmade", Namespace: "default"}}
	m, c := newManager(s, &orphan)

	err := m.reconcileTCPRoutes(context.Background(), s, nil)

	require.NoError(t, err)
	assert.Equal(t, []string{"handmade"}, routeNames(t, c, "default"))
}

// TCPRoutes are for TCP. An HTTP exposure gets an HTTPRoute elsewhere, and a
// TCPRoute here as well would bind the same listener twice.
func TestReconcileTCPRoutes_SkipsNonTCPExposures(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)
	web := sealsv1.Exposure{
		Name:     "web",
		Protocol: sealsv1.ProtocolHTTP,
		Backend:  sealsv1.Backend{Service: "redis", Port: 8080},
	}

	err := m.reconcileTCPRoutes(context.Background(), s,
		[]sealsv1.Exposure{tcpExposure("direct", 6379), web})
	require.NoError(t, err)

	assert.Equal(t, []string{"sample-direct"}, routeNames(t, c, "default"))
}

// An exposure that switched protocol keeps its old TCPRoute otherwise.
func TestReconcileTCPRoutes_DeletesTheRouteOfAnExposureThatTurnedHTTP(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)
	ctx := context.Background()

	require.NoError(t, m.reconcileTCPRoutes(ctx, s,
		[]sealsv1.Exposure{tcpExposure("direct", 6379)}))

	require.NoError(t, m.reconcileTCPRoutes(ctx, s, []sealsv1.Exposure{{
		Name:     "direct",
		Protocol: sealsv1.ProtocolHTTP,
		Backend:  sealsv1.Backend{Service: "redis", Port: 6379},
	}}))

	assert.Empty(t, routeNames(t, c, "default"))
}

// A build error is a caller bug. Writing the routes that did build would leave
// the Seal half exposed.
func TestReconcileTCPRoutes_ABadBackendIsAnErrorAndWritesNothing(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)
	broken := sealsv1.Exposure{Name: "admin", Protocol: sealsv1.ProtocolTCP}

	err := m.reconcileTCPRoutes(context.Background(), s,
		[]sealsv1.Exposure{tcpExposure("direct", 6379), broken})

	require.Error(t, err)
	assert.Empty(t, routeNames(t, c, "default"), "one bad exposure must not apply half the routes")
}

// A build error must not read as "no exposures, delete everything".
func TestReconcileTCPRoutes_ABadBackendDoesNotDeleteExistingRoutes(t *testing.T) {
	s := seal(1)
	existing := tcpRoute("sample-direct", "default", sealUID)
	m, c := newManager(s, &existing)
	broken := sealsv1.Exposure{Name: "admin", Protocol: sealsv1.ProtocolTCP}

	err := m.reconcileTCPRoutes(context.Background(), s, []sealsv1.Exposure{broken})

	require.Error(t, err)
	assert.Equal(t, []string{"sample-direct"}, routeNames(t, c, "default"),
		"a build error must not tear down working routes")
}

func TestReconcileTCPRoutes_ReapplyingTheSameExposuresIsStable(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)
	ctx := context.Background()
	want := []sealsv1.Exposure{tcpExposure("direct", 6379)}

	require.NoError(t, m.reconcileTCPRoutes(ctx, s, want))
	require.NoError(t, m.reconcileTCPRoutes(ctx, s, want))

	assert.Equal(t, []string{"sample-direct"}, routeNames(t, c, "default"))
}

func TestBuildExposureStatus_ReadyTCPExposure(t *testing.T) {
	got := buildExposureStatus(
		sealsv1.Exposure{Name: "direct", Protocol: sealsv1.ProtocolTCP},
		"tcp", exposureResult{Shard: "tcp-0", Address: "172.19.0.200", Port: 30007, RouteReady: true},
	)

	assert.Equal(t, "direct", got.Name)
	assert.Equal(t, "tcp", got.Class)
	assert.True(t, got.Ready)
	assert.Equal(t, "tcp-0", got.Shard)
	assert.Equal(t, "172.19.0.200", got.Address)
	assert.EqualValues(t, 30007, got.Port)
	assert.Equal(t, "tcp://172.19.0.200:30007", got.URL)
}

func TestBuildExposureStatus_HTTPIsNotSupportedYet(t *testing.T) {
	got := buildExposureStatus(
		sealsv1.Exposure{Name: "api", Protocol: sealsv1.ProtocolHTTPS},
		"public", exposureResult{},
	)

	assert.False(t, got.Ready)
	assert.Equal(t, "protocol HTTPS is not supported yet", got.Message)
	assert.Empty(t, got.URL)
}

func TestBuildExposureStatus_NoGatewayAddressYet(t *testing.T) {
	got := buildExposureStatus(
		sealsv1.Exposure{Name: "direct", Protocol: sealsv1.ProtocolTCP},
		"tcp", exposureResult{Shard: "tcp-0", Port: 30007, RouteReady: true},
	)

	assert.False(t, got.Ready)
	assert.Empty(t, got.URL, "tcp://:30007 is worse than no URL")
	assert.Equal(t, "waiting for gateway address", got.Message)
	assert.Equal(t, "tcp-0", got.Shard, "the shard is known before its address")
	assert.EqualValues(t, 30007, got.Port)
}

func TestBuildExposureStatus_RouteNotReadyCarriesTheRouteMessage(t *testing.T) {
	got := buildExposureStatus(
		sealsv1.Exposure{Name: "direct", Protocol: sealsv1.ProtocolTCP},
		"tcp", exposureResult{
			Shard: "tcp-0", Address: "172.19.0.200", Port: 30007,
			RouteMsg: "service redis not found",
		},
	)

	assert.False(t, got.Ready)
	assert.Equal(t, "service redis not found", got.Message, "the route's own message, not a made-up one")
}

func TestBuildExposureStatus_CarriesTheFailureMessage(t *testing.T) {
	got := buildExposureStatus(
		sealsv1.Exposure{Name: "direct", Protocol: sealsv1.ProtocolTCP},
		"", exposureResult{Err: errors.New("no shard in class tcp has capacity")},
	)

	assert.False(t, got.Ready)
	assert.Contains(t, got.Message, "no shard in class tcp has capacity")
}

func TestBuildExposureStatus_FailureWinsOverAReadyRoute(t *testing.T) {
	got := buildExposureStatus(
		sealsv1.Exposure{Name: "direct", Protocol: sealsv1.ProtocolTCP},
		"tcp", exposureResult{
			Shard: "tcp-0", Address: "172.19.0.200", Port: 30007, RouteReady: true,
			Err: errors.New("port 30007 still conflicts"),
		},
	)

	assert.False(t, got.Ready)
	assert.Equal(t, "port 30007 still conflicts", got.Message)
}

func TestBuildExposureStatus_CarriesTheConflictCount(t *testing.T) {
	got := buildExposureStatus(
		sealsv1.Exposure{Name: "direct", Protocol: sealsv1.ProtocolTCP},
		"tcp", exposureResult{Shard: "tcp-0", Port: 30007, ConflictCount: 2},
	)

	assert.EqualValues(t, 2, got.ConflictCount, "the count must survive to the next reconcile")
}

func readyOutcome(name string) exposureOutcome {
	return exposureOutcome{Status: sealsv1.ExposureStatus{Name: name, Ready: true}}
}

func TestRollUp_NoExposuresKeepsDoneAndANilList(t *testing.T) {
	done := phase{Phase: sealsv1.SealPhaseReady, Message: "networkpolicy applied"}

	got := rollUp(nil, done)

	assert.Equal(t, done, got)
	assert.Nil(t, got.Exposures)
}

func TestRollUp_AllReadyIsDone(t *testing.T) {
	got := rollUp(
		[]exposureOutcome{readyOutcome("a"), readyOutcome("b")},
		phase{Phase: sealsv1.SealPhaseReady, Message: "networkpolicy applied"},
	)

	assert.Equal(t, sealsv1.SealPhaseReady, got.Phase)
	assert.Equal(t, "networkpolicy applied", got.Message)
	require.Len(t, got.Exposures, 2)
	assert.Equal(t, "a", got.Exposures[0].Name)
	assert.Equal(t, "b", got.Exposures[1].Name)
}

func TestRollUp_WaitingIsPending(t *testing.T) {
	waiting := exposureOutcome{Status: sealsv1.ExposureStatus{Name: "b", Message: "waiting for gateway address"}}

	got := rollUp(
		[]exposureOutcome{readyOutcome("a"), waiting},
		phase{Phase: sealsv1.SealPhaseReady},
	)

	assert.Equal(t, sealsv1.SealPhasePending, got.Phase)
	assert.Equal(t, `exposure "b": waiting for gateway address`, got.Message)
}

func TestRollUp_FailedBeatsWaitingAndNamesTheFirstFailure(t *testing.T) {
	waiting := exposureOutcome{Status: sealsv1.ExposureStatus{Name: "a", Message: "waiting for gateway address"}}
	first := exposureOutcome{Status: sealsv1.ExposureStatus{Name: "b", Message: "no shard has room"}, Failed: true}
	second := exposureOutcome{Status: sealsv1.ExposureStatus{Name: "c", Message: "other"}, Failed: true}

	got := rollUp(
		[]exposureOutcome{waiting, first, second},
		phase{Phase: sealsv1.SealPhaseReady},
	)

	assert.Equal(t, sealsv1.SealPhaseFailed, got.Phase)
	assert.Equal(t, `exposure "b": no shard has room`, got.Message)
}

func TestRollUp_WaitingDoesNotHideAFailedPolicy(t *testing.T) {
	waiting := exposureOutcome{Status: sealsv1.ExposureStatus{Name: "a", Message: "waiting for gateway address"}}

	got := rollUp(
		[]exposureOutcome{waiting},
		phase{Phase: sealsv1.SealPhaseFailed, Message: "reconciling network policy: boom"},
	)

	assert.Equal(t, sealsv1.SealPhaseFailed, got.Phase)
	assert.Equal(t, "reconciling network policy: boom", got.Message)
	require.Len(t, got.Exposures, 1)
}

// readSetNamed fetches the ListenerSet name in the default namespace. The
// second result is false when there is none.
func readSetNamed(t *testing.T, c client.Client, name string) (*gwv1.ListenerSet, bool) {
	t.Helper()

	obj := &gwv1.ListenerSet{}
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, obj)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	require.NoError(t, err)

	return obj, true
}

// ownedSetNames lists the names of the ListenerSets the test Seal controls,
// sorted.
func ownedSetNames(t *testing.T, c client.Client) []string {
	t.Helper()

	list := &gwv1.ListenerSetList{}
	require.NoError(t, c.List(context.Background(), list, client.InNamespace("default")))

	names := []string{}
	for i := range list.Items {
		if metav1.IsControlledBy(&list.Items[i], seal(1)) {
			names = append(names, list.Items[i].GetName())
		}
	}
	sort.Strings(names)

	return names
}

func tcpListener(name string, port int32) gatewayapi.Listener {
	return gatewayapi.Listener{Name: name, Protocol: "TCP", Port: port}
}

func want(exposure, shard string, port int32) wantSet {
	return wantSet{Exposure: exposure, Parent: tcpRef(shard), Port: port}
}

// directSet is the set of exposure "direct" on tcp-0.
func directSet(t *testing.T, port int64, conds ...metav1.Condition) *gwv1.ListenerSet {
	t.Helper()

	return ownedSet(t, "sample-direct", "direct", "tcp-0", port, conds...)
}

func TestReconcileListenerSets_AppliesOneSetPerExposure(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileListenerSets(context.Background(), s,
		[]wantSet{want("direct", "tcp-0", 30007), want("admin", "tcp-0", 30008)}, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"sample-admin", "sample-direct"}, ownedSetNames(t, c))
}

func TestReconcileListenerSets_EachSetHoldsOneListenerNamedAfterTheExposure(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileListenerSets(context.Background(), s,
		[]wantSet{want("direct", "tcp-0", 30007), want("admin", "tcp-0", 30008)}, nil)
	require.NoError(t, err)

	direct, found := readSetNamed(t, c, "sample-direct")
	require.True(t, found)
	assert.Equal(t, []gatewayapi.Listener{tcpListener("direct", 30007)}, gatewayapi.Listeners(direct))

	admin, found := readSetNamed(t, c, "sample-admin")
	require.True(t, found)
	assert.Equal(t, []gatewayapi.Listener{tcpListener("admin", 30008)}, gatewayapi.Listeners(admin))
}

func TestReconcileListenerSets_EachSetHasItsOwnShard(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileListenerSets(context.Background(), s,
		[]wantSet{want("direct", "tcp-0", 30007), want("admin", "tcp-1", 30007)}, nil)
	require.NoError(t, err)

	direct, found := readSetNamed(t, c, "sample-direct")
	require.True(t, found)
	admin, found := readSetNamed(t, c, "sample-admin")
	require.True(t, found)

	got, ok := gatewayapi.ParentShard(direct)
	require.True(t, ok)
	assert.Equal(t, tcpRef("tcp-0"), got)

	got, ok = gatewayapi.ParentShard(admin)
	require.True(t, ok)
	assert.Equal(t, tcpRef("tcp-1"), got)
}

// Without blockOwnerDeletion the listener outlives the Seal and its port stays
// claimed forever.
func TestReconcileListenerSets_SetsTheControllerOwnerReference(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	require.NoError(t, m.reconcileListenerSets(context.Background(), s,
		[]wantSet{want("direct", "tcp-0", 30007)}, nil))

	obj, found := readSetNamed(t, c, "sample-direct")
	require.True(t, found)

	refs := obj.GetOwnerReferences()
	require.Len(t, refs, 1)
	assert.Equal(t, "Seal", refs[0].Kind)
	assert.Equal(t, "sample", refs[0].Name)
	assert.Equal(t, sealUID, refs[0].UID)
	assert.True(t, *refs[0].Controller)
	assert.True(t, *refs[0].BlockOwnerDeletion)
}

func TestReconcileListenerSets_DeletesTheSetOfADroppedExposure(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)
	ctx := context.Background()

	require.NoError(t, m.reconcileListenerSets(ctx, s,
		[]wantSet{want("direct", "tcp-0", 30007), want("admin", "tcp-0", 30008)}, nil))
	require.NoError(t, m.reconcileListenerSets(ctx, s,
		[]wantSet{want("direct", "tcp-0", 30007)}, nil))

	assert.Equal(t, []string{"sample-direct"}, ownedSetNames(t, c),
		"a dropped exposure's set must go, or its port stays taken")
}

// This is the upgrade path from one set per Seal.
func TestReconcileListenerSets_DeletesTheOldSealNamedSet(t *testing.T) {
	s := seal(1)
	m, c := newManager(s, ownedSet(t, "sample", "direct", "tcp-0", 30007))

	require.NoError(t, m.reconcileListenerSets(context.Background(), s,
		[]wantSet{want("direct", "tcp-0", 30100)}, nil))

	assert.Equal(t, []string{"sample-direct"}, ownedSetNames(t, c))
}

// A kept set belongs to an exposure whose class is gone or that gave up on
// conflicts. Its live traffic must survive.
func TestReconcileListenerSets_KeepsASetMarkedKeep(t *testing.T) {
	s := seal(1)
	m, c := newManager(s, directSet(t, 30007))

	require.NoError(t, m.reconcileListenerSets(context.Background(), s,
		[]wantSet{want("admin", "tcp-0", 30008)}, []string{"direct"}))

	assert.Equal(t, []string{"sample-admin", "sample-direct"}, ownedSetNames(t, c))
	assert.EqualValues(t, 30007, exposurePort(t, c, "direct"))
}

// spec.listeners requires at least one entry, so there is no such thing as an
// empty ListenerSet to apply.
func TestReconcileListenerSets_NothingWantedDeletesEveryOwnedSet(t *testing.T) {
	s := seal(1)
	m, c := newManager(s, directSet(t, 30007), ownedSet(t, "sample-admin", "admin", "tcp-0", 30008))

	require.NoError(t, m.reconcileListenerSets(context.Background(), s, nil, nil))

	assert.Empty(t, ownedSetNames(t, c))
}

func TestReconcileListenerSets_NothingWantedAndNothingOwnedIsNotAnError(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	require.NoError(t, m.reconcileListenerSets(context.Background(), s, nil, nil))
	assert.Empty(t, ownedSetNames(t, c))
}

func TestReconcileListenerSets_LeavesAnotherSealsSetAlone(t *testing.T) {
	s := seal(1)
	other := ownedSet(t, "other-direct", "direct", "tcp-0", 30009)
	other.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: sealsv1.GroupVersion.String(),
		Kind:       "Seal",
		Name:       "other",
		UID:        types.UID("99999999-2222-3333-4444-555555555555"),
		Controller: ptr.To(true),
	}})
	m, c := newManager(s, other)

	require.NoError(t, m.reconcileListenerSets(context.Background(), s, nil, nil))

	_, found := readSetNamed(t, c, "other-direct")
	assert.True(t, found)
}

func TestReconcileListenerSets_LeavesAnUnownedSetAlone(t *testing.T) {
	s := seal(1)
	unowned := listenerSet(t, "handmade", "default", "tcp-0", 30009)
	m, c := newManager(s, &unowned)

	require.NoError(t, m.reconcileListenerSets(context.Background(), s, nil, nil))

	_, found := readSetNamed(t, c, "handmade")
	assert.True(t, found)
}

func TestReconcileListenerSets_ReapplyingIsStable(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)
	ctx := context.Background()
	sets := []wantSet{want("direct", "tcp-0", 30007)}

	require.NoError(t, m.reconcileListenerSets(ctx, s, sets, nil))
	require.NoError(t, m.reconcileListenerSets(ctx, s, sets, nil))

	assert.Equal(t, []string{"sample-direct"}, ownedSetNames(t, c))
	assert.EqualValues(t, 30007, exposurePort(t, c, "direct"),
		"a settled Seal must not churn its own published ports")
}

// A bad port is a caller bug. Nothing may be written, not even the good sets.
func TestReconcileListenerSets_ABadPortIsAnErrorAndWritesNothing(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileListenerSets(context.Background(), s,
		[]wantSet{want("direct", "tcp-0", 30007), want("admin", "tcp-0", 0)}, nil)

	require.Error(t, err)
	assert.Empty(t, ownedSetNames(t, c))
}

// Deleting would take live addresses down over a caller bug.
func TestReconcileListenerSets_ABadPortDoesNotDeleteExistingSets(t *testing.T) {
	s := seal(1)
	m, c := newManager(s, directSet(t, 30007))

	err := m.reconcileListenerSets(context.Background(), s,
		[]wantSet{want("admin", "tcp-0", 70000)}, nil)

	require.Error(t, err)
	assert.Equal(t, []string{"sample-direct"}, ownedSetNames(t, c))
}

func TestOwnedSets_KeepsOnlyTheSealsSets(t *testing.T) {
	mine := directSet(t, 30007)
	other := listenerSet(t, "handmade", "default", "tcp-0", 30009)

	got := ownedSets([]gwv1.ListenerSet{*mine, other}, seal(1))

	require.Len(t, got, 1)
	assert.Contains(t, got, "sample-direct")
}

func TestOwnedSets_EmptyInput(t *testing.T) {
	assert.Empty(t, ownedSets(nil, seal(1)))
}

func TestReadOwnedSets_ListsOnlyTheSealsSetsInItsNamespace(t *testing.T) {
	s := seal(1)
	elsewhere := ownedSet(t, "sample-admin", "admin", "tcp-0", 30008)
	elsewhere.SetNamespace("other-ns")
	unowned := listenerSet(t, "handmade", "default", "tcp-0", 30009)
	m, _ := newManager(s, directSet(t, 30007), elsewhere, &unowned)

	got, err := readOwnedSets(context.Background(), m.Client, s)

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Contains(t, got, "sample-direct")
}

func TestReadOwnedSets_NoSetsIsAnEmptyMap(t *testing.T) {
	s := seal(1)
	m, _ := newManager(s)

	got, err := readOwnedSets(context.Background(), m.Client, s)

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestListenerPort_FindsTheExposure(t *testing.T) {
	port, ok := listenerPort(directSet(t, 30007), "direct")

	assert.True(t, ok)
	assert.EqualValues(t, 30007, port)
}

func TestListenerPort_OtherNameOrNoSet(t *testing.T) {
	_, ok := listenerPort(directSet(t, 30007), "admin")
	assert.False(t, ok)

	_, ok = listenerPort(nil, "direct")
	assert.False(t, ok)
}

func TestNewPlacer_CountsListenersPerShard(t *testing.T) {
	sets := []gwv1.ListenerSet{
		listenerSet(t, "a", "ns", "tcp-0", 30000, 30001),
		listenerSet(t, "b", "ns", "tcp-1", 30002),
	}

	p := newPlacer(sets)

	assert.Equal(t, 2, p.counts[tcpRef("tcp-0")])
	assert.Equal(t, 1, p.counts[tcpRef("tcp-1")])
}

func TestNewPlacer_NoSets(t *testing.T) {
	p := newPlacer(nil)

	assert.Empty(t, p.counts)
	assert.NotNil(t, p.takenOn(tcpRef("tcp-0"), nil, nil))
	assert.Empty(t, p.takenOn(tcpRef("tcp-0"), nil, nil))
}

func TestPlacerTakenOn_HoldsOtherSetsPortsOnTheShard(t *testing.T) {
	sets := []gwv1.ListenerSet{
		listenerSet(t, "a", "ns", "tcp-0", 30000),
		listenerSet(t, "b", "ns", "tcp-1", 30001),
	}

	taken := newPlacer(sets).takenOn(tcpRef("tcp-0"), nil, nil)

	assert.Equal(t, map[int32]struct{}{30000: {}}, taken)
}

// A sticky exposure re-picks nothing, but a move on the same shard may reuse
// its own old port.
func TestPlacerTakenOn_FreesTheOwnPort(t *testing.T) {
	own := directSet(t, 30007)
	p := newPlacer([]gwv1.ListenerSet{*own})

	assert.Empty(t, p.takenOn(tcpRef("tcp-0"), own, nil))
}

func TestPlacerTakenOn_KeepsAConflictedOwnPort(t *testing.T) {
	own := directSet(t, 30007)
	p := newPlacer([]gwv1.ListenerSet{*own})

	taken := p.takenOn(tcpRef("tcp-0"), own, map[string]struct{}{"direct": {}})

	assert.Contains(t, taken, int32(30007), "handing it back would reproduce the conflict")
}

func TestPlacerTakenOn_SiblingPortsAreTaken(t *testing.T) {
	own := directSet(t, 30007)
	sibling := ownedSet(t, "sample-admin", "admin", "tcp-0", 30008)
	p := newPlacer([]gwv1.ListenerSet{*own, *sibling})

	taken := p.takenOn(tcpRef("tcp-0"), own, nil)

	assert.Equal(t, map[int32]struct{}{30008: {}}, taken)
}

func TestPlacerClaim_PortIsTakenOnThatShardOnly(t *testing.T) {
	p := newPlacer(nil)

	p.claim(tcpRef("tcp-0"), 30007, true)

	assert.Contains(t, p.takenOn(tcpRef("tcp-0"), nil, nil), int32(30007))
	assert.NotContains(t, p.takenOn(tcpRef("tcp-1"), nil, nil), int32(30007),
		"each shard has its own IP, so the same port elsewhere is free")
}

func TestPlacerClaim_AMoveCountsAListener(t *testing.T) {
	p := newPlacer(nil)

	p.claim(tcpRef("tcp-0"), 30007, true)
	p.claim(tcpRef("tcp-0"), 30008, true)

	assert.Equal(t, 2, p.counts[tcpRef("tcp-0")])
}

func TestPlacerClaim_AStickyExposureDoesNotCountTwice(t *testing.T) {
	own := directSet(t, 30007)
	p := newPlacer([]gwv1.ListenerSet{*own})

	p.claim(tcpRef("tcp-0"), 30007, false)

	assert.Equal(t, 1, p.counts[tcpRef("tcp-0")])
}

// Callers add to the result, so it must be a copy.
func TestPlacerTakenOn_DoesNotChangeThePlacer(t *testing.T) {
	p := newPlacer(nil)
	p.claim(tcpRef("tcp-0"), 30007, true)

	taken := p.takenOn(tcpRef("tcp-0"), nil, nil)
	taken[30008] = struct{}{}

	assert.NotContains(t, p.takenOn(tcpRef("tcp-0"), nil, nil), int32(30008))
}
