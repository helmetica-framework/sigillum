package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
