package controllers

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	"github.com/helmetica-framework/sigillum/controllers/gatewayapi"
)

// readListenerSet fetches the ListenerSet a Seal owns. The second result is
// false when there is none.
func readListenerSet(t *testing.T, c client.Client, seal *sealsv1.Seal) (*unstructured.Unstructured, bool) {
	t.Helper()

	obj := gatewayapi.New(gatewayapi.ListenerSetGVK)
	err := c.Get(context.Background(), client.ObjectKeyFromObject(seal), obj)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	require.NoError(t, err)

	return obj, true
}

func tcpListener(name string, port int32) gatewayapi.Listener {
	return gatewayapi.Listener{Name: name, Protocol: "TCP", Port: port}
}

func TestReconcileListenerSet_AppliesOneSetNamedAfterTheSeal(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileListenerSet(context.Background(), s, tcpRef("tcp-0"),
		[]gatewayapi.Listener{tcpListener("direct", 30007)})
	require.NoError(t, err)

	obj, found := readListenerSet(t, c, s)

	require.True(t, found, "one ListenerSet per Seal, named after the Seal")
	assert.Equal(t, "sample", obj.GetName())
	assert.Equal(t, "default", obj.GetNamespace())
}

func TestReconcileListenerSet_WritesEveryListener(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileListenerSet(context.Background(), s, tcpRef("tcp-0"),
		[]gatewayapi.Listener{tcpListener("direct", 30007), tcpListener("admin", 30008)})
	require.NoError(t, err)

	obj, found := readListenerSet(t, c, s)
	require.True(t, found)

	assert.Equal(t, []gatewayapi.Listener{
		{Name: "direct", Protocol: "TCP", Port: 30007},
		{Name: "admin", Protocol: "TCP", Port: 30008},
	}, gatewayapi.Listeners(obj))
}

func TestReconcileListenerSet_ParentsToTheGivenShard(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileListenerSet(context.Background(), s, tcpRef("tcp-1"),
		[]gatewayapi.Listener{tcpListener("direct", 30007)})
	require.NoError(t, err)

	obj, found := readListenerSet(t, c, s)
	require.True(t, found)

	parent, ok := gatewayapi.ParentShard(obj)

	require.True(t, ok)
	assert.Equal(t, tcpRef("tcp-1"), parent)
}

// Without blockOwnerDeletion the listener outlives the Seal and its port stays
// claimed forever.
func TestReconcileListenerSet_SetsTheControllerOwnerReference(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileListenerSet(context.Background(), s, tcpRef("tcp-0"),
		[]gatewayapi.Listener{tcpListener("direct", 30007)})
	require.NoError(t, err)

	obj, found := readListenerSet(t, c, s)
	require.True(t, found)

	refs := obj.GetOwnerReferences()

	require.Len(t, refs, 1)
	assert.Equal(t, "Seal", refs[0].Kind)
	assert.Equal(t, "sample", refs[0].Name)
	assert.Equal(t, sealUID, refs[0].UID)
	assert.True(t, *refs[0].Controller)
	assert.True(t, *refs[0].BlockOwnerDeletion)
}

// spec.listeners requires at least one entry, so there is no such thing as an
// empty ListenerSet to apply.
func TestReconcileListenerSet_NoListenersDeletesAnExistingSet(t *testing.T) {
	s := seal(1)
	existing := listenerSet(t, "sample", "default", "tcp-0", 30007)
	m, c := newManager(s, &existing)

	err := m.reconcileListenerSet(context.Background(), s, tcpRef("tcp-0"), nil)
	require.NoError(t, err)

	_, found := readListenerSet(t, c, s)

	assert.False(t, found, "the last exposure going away must remove the object")
}

func TestReconcileListenerSet_NoListenersAndNoSetIsNotAnError(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileListenerSet(context.Background(), s, tcpRef("tcp-0"), nil)

	require.NoError(t, err, "a Seal that never had a TCP exposure reconciles cleanly")
	_, found := readListenerSet(t, c, s)
	assert.False(t, found)
}

// Apply is the whole reason a dropped exposure cleans itself up: a listener this
// field manager stops sending is removed from the object.
func TestReconcileListenerSet_DroppingAListenerRemovesIt(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)
	ctx := context.Background()

	require.NoError(t, m.reconcileListenerSet(ctx, s, tcpRef("tcp-0"),
		[]gatewayapi.Listener{tcpListener("direct", 30007), tcpListener("admin", 30008)}))

	require.NoError(t, m.reconcileListenerSet(ctx, s, tcpRef("tcp-0"),
		[]gatewayapi.Listener{tcpListener("direct", 30007)}))

	obj, found := readListenerSet(t, c, s)
	require.True(t, found)

	assert.Equal(t, []gatewayapi.Listener{{Name: "direct", Protocol: "TCP", Port: 30007}},
		gatewayapi.Listeners(obj))
}

func TestReconcileListenerSet_ReapplyingTheSameListenersIsStable(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)
	ctx := context.Background()
	want := []gatewayapi.Listener{tcpListener("direct", 30007)}

	require.NoError(t, m.reconcileListenerSet(ctx, s, tcpRef("tcp-0"), want))
	require.NoError(t, m.reconcileListenerSet(ctx, s, tcpRef("tcp-0"), want))

	obj, found := readListenerSet(t, c, s)
	require.True(t, found)

	assert.Equal(t, want, gatewayapi.Listeners(obj),
		"a settled Seal must not churn its own published ports")
}

// A bad port is a caller bug, not a cluster state. It must fail loud rather than
// read as "no listeners, delete the set".
func TestReconcileListenerSet_ABadPortIsAnErrorAndWritesNothing(t *testing.T) {
	s := seal(1)
	m, c := newManager(s)

	err := m.reconcileListenerSet(context.Background(), s, tcpRef("tcp-0"),
		[]gatewayapi.Listener{tcpListener("direct", 0)})

	require.Error(t, err)
	_, found := readListenerSet(t, c, s)
	assert.False(t, found, "a rejected build must not write a partial object")
}

// The set already exists and the new build fails. Deleting it would take a live
// address down over a caller bug.
func TestReconcileListenerSet_ABadPortDoesNotDeleteTheExistingSet(t *testing.T) {
	s := seal(1)
	existing := listenerSet(t, "sample", "default", "tcp-0", 30007)
	m, c := newManager(s, &existing)

	err := m.reconcileListenerSet(context.Background(), s, tcpRef("tcp-0"),
		[]gatewayapi.Listener{tcpListener("direct", 70000)})

	require.Error(t, err)
	_, found := readListenerSet(t, c, s)
	assert.True(t, found, "a build error must not tear down working listeners")
}

// tcpExposure is one TCP exposure pointing at the redis service.
func tcpExposure(name string, port int32) sealsv1.Exposure {
	return sealsv1.Exposure{
		Name:     name,
		Protocol: sealsv1.ProtocolTCP,
		Backend:  sealsv1.Backend{Service: "redis", Port: port},
	}
}

// tcpRoute seeds a TCPRoute owned by the Seal carrying uid.
func tcpRoute(name, namespace string, uid types.UID) unstructured.Unstructured {
	obj := gatewayapi.New(gatewayapi.TCPRouteGVK)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	obj.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion:         sealsv1.GroupVersion.String(),
		Kind:               "Seal",
		Name:               "whoever",
		UID:                uid,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}})

	return *obj
}

// routeNames lists every TCPRoute name in namespace, sorted.
func routeNames(t *testing.T, c client.Client, namespace string) []string {
	t.Helper()

	list := gatewayapi.NewList(gatewayapi.TCPRouteGVK)
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
func readTCPRoute(t *testing.T, c client.Client, name, namespace string) (*unstructured.Unstructured, bool) {
	t.Helper()

	obj := gatewayapi.New(gatewayapi.TCPRouteGVK)
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

	rules, found, err := unstructured.NestedSlice(obj.Object, "spec", "rules")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, rules, 1)

	refs, ok := rules[0].(map[string]any)["backendRefs"].([]any)
	require.True(t, ok)
	require.Len(t, refs, 1)

	b := refs[0].(map[string]any)
	assert.Equal(t, "redis", b["name"])
	assert.Equal(t, int64(6379), b["port"])
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

	parents, found, err := unstructured.NestedSlice(obj.Object, "spec", "parentRefs")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, parents, 1)

	p := parents[0].(map[string]any)
	assert.Equal(t, "ListenerSet", p["kind"])
	assert.Equal(t, "sample", p["name"])
	assert.Equal(t, "direct", p["sectionName"])
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
	orphan := *gatewayapi.New(gatewayapi.TCPRouteGVK)
	orphan.SetName("handmade")
	orphan.SetNamespace("default")
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
