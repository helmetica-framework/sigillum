package controllers

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/managedfields"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	netv1ac "k8s.io/client-go/applyconfigurations/networking/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	sealsac "github.com/helmetica-framework/sigillum/applyconfiguration"
	"github.com/helmetica-framework/sigillum/controllers/gatewayapi"
)

func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(sealsv1.AddToScheme(scheme))
	utilruntime.Must(gatewayapi.Register(scheme))
	return scheme
}

func seal(generation int64) *sealsv1.Seal {
	return &sealsv1.Seal{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "sample",
			Namespace:  "default",
			Generation: generation,
			UID:        sealUID,
		},
		Spec: sealsv1.SealSpec{},
	}
}

// sealUID stands in for the UID the API server would assign. The controller
// copies it into the policy's owner reference, and an owner reference without
// one is rejected in a real cluster.
const sealUID = types.UID("11111111-2222-3333-4444-555555555555")

func sealKey() types.NamespacedName {
	return types.NamespacedName{Name: "sample", Namespace: "default"}
}

func policyKey() types.NamespacedName {
	return types.NamespacedName{Name: "allow-service-access", Namespace: "default"}
}

// namespace builds the namespace the seal lives in. desiredPhase reads it on
// every reconcile, so it has to be in the fake client's object set.
func namespace(annotations map[string]string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "default",
			Annotations: annotations,
		},
	}
}

// newManager wires a SealManager over a fake client seeded with objs plus an
// unannotated "default" namespace.
func newManager(objs ...client.Object) (*SealManager, client.Client) {
	return newManagerInNamespace(namespace(nil), objs...)
}

// newManagerInNamespace is newManager with control over the namespace object,
// so a test can set the claim annotation on it. The status subresource must be
// registered or every status write is rejected.
func newManagerInNamespace(ns *corev1.Namespace, objs ...client.Object) (*SealManager, client.Client) {
	scheme := newTestScheme()
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sealsv1.Seal{}).
		// Seal applies are checked against the generated schema. Anything the
		// generated converter does not know falls through to the deduced one,
		// which is what NetworkPolicy needs: the fake client still lists it in
		// inTreeResourcesWithStatus and injects a status, but
		// NetworkPolicyStatus was removed from the API, so the client-go
		// schema declares no status field and its typed converter rejects the
		// object. Were that converter in this list, one side of the merge
		// would use it and the other the deduced one, and the merge would fail
		// on mismatched schemas. A real API server is unaffected: NetworkPolicy
		// has no status subresource there.
		WithTypeConverters(
			sealsac.NewTypeConverter(scheme),
			managedfields.NewDeducedTypeConverter(),
		).
		// Apply is the write path under test, so the field manager it records
		// has to survive the read back.
		WithReturnManagedFields().
		WithObjects(append([]client.Object{ns}, objs...)...).
		Build()

	return &SealManager{
		Client: c,
		// The fake client serves both roles here. A test that needs the
		// cached and uncached views to differ builds its own manager.
		APIReader: c,
		Scheme:    scheme,
		Log:       logr.Discard(),
	}, c
}

// ingressPeersOf reconciles and returns the peers of the single ingress rule
// on the resulting NetworkPolicy.
func ingressPeersOf(t *testing.T, c client.Client) []netv1.NetworkPolicyPeer {
	t.Helper()

	np := &netv1.NetworkPolicy{}
	require.NoError(t, c.Get(context.Background(), policyKey(), np))
	require.Len(t, np.Spec.Ingress, 1, "the policy carries exactly one ingress rule")

	return np.Spec.Ingress[0].From
}

func TestReconcile_SealGoneIsNoError(t *testing.T) {
	m, _ := newManager()

	res, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, res)
}

func TestReconcile_SetsReady(t *testing.T) {
	m, c := newManager(seal(1))

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	got := &sealsv1.Seal{}
	require.NoError(t, c.Get(context.Background(), sealKey(), got))
	assert.Equal(t, sealsv1.SealPhaseReady, got.Status.Phase)
	assert.Equal(t, int64(1), got.Status.ObservedGeneration)
	assert.Equal(t, "empty config, skipping provisioning", got.Status.Message)
}

func TestReconcile_IsIdempotent(t *testing.T) {
	m, c := newManager(seal(1))
	ctx := context.Background()

	_, err := m.Reconcile(ctx, ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	afterFirst := &sealsv1.Seal{}
	require.NoError(t, c.Get(ctx, sealKey(), afterFirst))

	_, err = m.Reconcile(ctx, ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	afterSecond := &sealsv1.Seal{}
	require.NoError(t, c.Get(ctx, sealKey(), afterSecond))

	assert.Equal(t, afterFirst.ResourceVersion, afterSecond.ResourceVersion,
		"a settled seal must not be written again")
}

func TestReconcile_SpecChangeRestampsObservedGeneration(t *testing.T) {
	settled := seal(1)
	settled.Status = sealsv1.SealStatus{
		Phase:              sealsv1.SealPhaseReady,
		ObservedGeneration: 1,
	}
	m, c := newManager(settled)
	ctx := context.Background()

	// Simulate a spec edit: the API server bumps generation, status lags.
	current := &sealsv1.Seal{}
	require.NoError(t, c.Get(ctx, sealKey(), current))
	current.Generation = 2
	current.Spec.AllowedNamespaces = []string{"other-value"}
	require.NoError(t, c.Update(ctx, current))

	_, err := m.Reconcile(ctx, ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	got := &sealsv1.Seal{}
	require.NoError(t, c.Get(ctx, sealKey(), got))
	assert.Equal(t, int64(2), got.Status.ObservedGeneration,
		"observedGeneration must follow the spec generation")
	assert.Equal(t, sealsv1.SealPhaseReady, got.Status.Phase)
}

// TestReconcile_PhaseChangeAloneTriggersWrite pins the phase term of the
// status-write guard. The generation term and the message term both match
// here (ObservedGeneration equals Generation, Message is empty on both
// sides), so only a divergent Phase can make Reconcile write. Without the
// phase term the guard would treat this seal as already settled and skip
// the write, leaving it stuck on SealPhaseFailed.
func TestReconcile_PhaseChangeAloneTriggersWrite(t *testing.T) {
	settled := seal(1)
	settled.Status = sealsv1.SealStatus{
		Phase:              sealsv1.SealPhaseFailed,
		ObservedGeneration: 1,
	}
	m, c := newManager(settled)
	ctx := context.Background()

	_, err := m.Reconcile(ctx, ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	got := &sealsv1.Seal{}
	require.NoError(t, c.Get(ctx, sealKey(), got))
	assert.Equal(t, sealsv1.SealPhaseReady, got.Status.Phase,
		"a phase mismatch alone must trigger a status write")
}

func TestReconcile_DeletingSealIsSkipped(t *testing.T) {
	deleting := seal(1)
	// The fake client drops an object that has a deletion timestamp and no
	// finalizer, so the fixture carries a dummy one. The controller does not
	// manage this finalizer.
	deleting.Finalizers = []string{"test.sigillum/keep-alive"}
	m, c := newManager(deleting)
	ctx := context.Background()

	require.NoError(t, c.Delete(ctx, deleting))

	_, err := m.Reconcile(ctx, ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	got := &sealsv1.Seal{}
	require.NoError(t, c.Get(ctx, sealKey(), got))
	assert.Empty(t, got.Status.Phase, "a seal being deleted must not be touched")
}

// allNamespacesPeer is what "allow access from everywhere" compiles down to:
// a namespaceSelector that matches no labels, and so matches every namespace.
var allNamespacesPeer = netv1.NetworkPolicyPeer{
	NamespaceSelector: &metav1.LabelSelector{},
}

func TestReconcile_AllowAllNamespacesOpensThePolicyToEveryNamespace(t *testing.T) {
	s := seal(1)
	s.Spec.AllowAllNamespaces = ptr.To(true)
	m, c := newManager(s)

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	assert.Equal(t, []netv1.NetworkPolicyPeer{allNamespacesPeer}, ingressPeersOf(t, c))

	got := &sealsv1.Seal{}
	require.NoError(t, c.Get(context.Background(), sealKey(), got))
	assert.Equal(t, sealsv1.SealPhaseReady, got.Status.Phase)
	assert.Equal(t, "networkpolicy applied", got.Status.Message,
		"allow-all must provision a policy, not fall into the empty-config skip")
}

func TestReconcile_AllowAllNamespacesReplacesAllowedNamespaces(t *testing.T) {
	s := seal(1)
	s.Spec.AllowAllNamespaces = ptr.To(true)
	s.Spec.AllowedNamespaces = []string{"team-a", "team-b"}
	m, c := newManager(s)

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	assert.Equal(t, []netv1.NetworkPolicyPeer{allNamespacesPeer}, ingressPeersOf(t, c),
		"allow-all wins outright: the named peers are redundant and must not be emitted")
}

func TestReconcile_AllowAllNamespacesReplacesTheClaimNamespacePeer(t *testing.T) {
	s := seal(1)
	s.Spec.AllowAllNamespaces = ptr.To(true)
	m, c := newManagerInNamespace(
		namespace(map[string]string{claimNamespaceAnnotation: "claimed"}),
		s,
	)

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	assert.Equal(t, []netv1.NetworkPolicyPeer{allNamespacesPeer}, ingressPeersOf(t, c),
		"allow-all wins over the claim namespace too")
}

func TestReconcile_AllowAllNamespacesFalseKeepsTheNamedPeers(t *testing.T) {
	s := seal(1)
	s.Spec.AllowAllNamespaces = ptr.To(false)
	s.Spec.AllowedNamespaces = []string{"team-a"}
	m, c := newManager(s)

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	assert.Equal(t, []netv1.NetworkPolicyPeer{expectedNSPeer("team-a")}, ingressPeersOf(t, c),
		"an explicit false must behave exactly like an unset field")
}

func TestReconcile_EmptyConfigWritesNoPolicy(t *testing.T) {
	m, c := newManager(seal(1))

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	err = c.Get(context.Background(), policyKey(), &netv1.NetworkPolicy{})
	assert.True(t, apierrors.IsNotFound(err),
		"no annotation and no allowed namespaces must not produce an allow-all by accident")
}

// TestReconcile_PolicyIsAppliedBySigillum pins the write path itself: the
// policy must arrive via server-side apply under the "sigillum" field
// manager. A plain create/update writes under a different manager with a
// different operation, so this fails for anything but Apply.
func TestReconcile_PolicyIsAppliedBySigillum(t *testing.T) {
	s := seal(1)
	s.Spec.AllowedNamespaces = []string{"team-a"}
	m, c := newManager(s)

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	np := &netv1.NetworkPolicy{}
	require.NoError(t, c.Get(context.Background(), policyKey(), np))

	var managers []string
	for _, mf := range np.GetManagedFields() {
		if mf.Operation == metav1.ManagedFieldsOperationApply {
			managers = append(managers, mf.Manager)
		}
	}
	assert.Contains(t, managers, "sigillum",
		"the policy must be written by an apply from the sigillum field manager")
}

// expectedNSPeer is the peer a test asserts on. It mirrors nsPeer, which now
// builds an apply configuration rather than an API object.
func expectedNSPeer(ns string) netv1.NetworkPolicyPeer {
	return netv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{kubeMetadataNameLabel: ns},
		},
	}
}

// TestReconcile_PolicySelectsEveryPodInTheNamespace pins the podSelector.
// An empty selector matches every pod, which is what makes the policy cover
// the whole namespace. Server-side apply only sends fields that were set, so
// this stays correct only while the controller sets podSelector explicitly.
func TestReconcile_PolicySelectsEveryPodInTheNamespace(t *testing.T) {
	s := seal(1)
	s.Spec.AllowedNamespaces = []string{"team-a"}
	m, c := newManager(s)

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	np := &netv1.NetworkPolicy{}
	require.NoError(t, c.Get(context.Background(), policyKey(), np))
	assert.Equal(t, metav1.LabelSelector{}, np.Spec.PodSelector,
		"an empty podSelector is what makes the policy apply to the whole namespace")
}

// TestReconcile_PolicyIsOwnedByTheSeal pins the controller owner reference.
// It is what garbage-collects the policy with the seal, and what lets the
// Owns(&netv1.NetworkPolicy{}) watch map a policy event back to its seal.
func TestReconcile_PolicyIsOwnedByTheSeal(t *testing.T) {
	s := seal(1)
	s.Spec.AllowedNamespaces = []string{"team-a"}
	m, c := newManager(s)

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	np := &netv1.NetworkPolicy{}
	require.NoError(t, c.Get(context.Background(), policyKey(), np))

	require.Len(t, np.GetOwnerReferences(), 1)
	ref := np.GetOwnerReferences()[0]
	assert.Equal(t, "Seal", ref.Kind)
	assert.Equal(t, sealsv1.GroupVersion.String(), ref.APIVersion)
	assert.Equal(t, "sample", ref.Name)
	assert.Equal(t, sealUID, ref.UID, "an owner reference without a UID is rejected by the API server")
	assert.True(t, ptr.Deref(ref.Controller, false), "the seal must be the controlling owner")
}

// TestReconcile_NarrowingAllowedNamespacesDropsThePeer pins that the ingress
// rule is replaced rather than merged into. Removing a namespace from the
// spec must remove its peer, not leave a stale one behind.
func TestReconcile_NarrowingAllowedNamespacesDropsThePeer(t *testing.T) {
	s := seal(1)
	s.Spec.AllowedNamespaces = []string{"team-a", "team-b"}
	m, c := newManager(s)
	ctx := context.Background()

	_, err := m.Reconcile(ctx, ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)
	require.Len(t, ingressPeersOf(t, c), 2)

	current := &sealsv1.Seal{}
	require.NoError(t, c.Get(ctx, sealKey(), current))
	current.Generation = 2
	current.Spec.AllowedNamespaces = []string{"team-a"}
	require.NoError(t, c.Update(ctx, current))

	_, err = m.Reconcile(ctx, ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	assert.Equal(t, []netv1.NetworkPolicyPeer{expectedNSPeer("team-a")}, ingressPeersOf(t, c),
		"the dropped namespace must not survive as a stale peer")
}

// TestReconcile_StatusIsAppliedBySigillum pins the status write path: the
// status must arrive via a server-side apply on the status subresource, under
// the "sigillum" field manager. A Status().Update writes under a different
// manager with a different operation, so this fails for anything but Apply.
func TestReconcile_StatusIsAppliedBySigillum(t *testing.T) {
	m, c := newManager(seal(1))

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	got := &sealsv1.Seal{}
	require.NoError(t, c.Get(context.Background(), sealKey(), got))

	// The entry is matched on the fields it owns rather than on
	// mf.Subresource: the fake client does not stamp the subresource on the
	// managed-fields entry, though a real API server records "status" there.
	var owned string
	for _, mf := range got.GetManagedFields() {
		if mf.Manager == "sigillum" && mf.Operation == metav1.ManagedFieldsOperationApply {
			owned = string(mf.FieldsV1.Raw)
		}
	}
	require.NotEmpty(t, owned, "the status must be written by an apply from sigillum")
	assert.Contains(t, owned, "f:phase", "sigillum must own status.phase")
	assert.Contains(t, owned, "f:observedGeneration", "sigillum must own status.observedGeneration")
	assert.Contains(t, owned, "f:message", "sigillum must own status.message")
}

func gatewaySel(ns, app string) sealsv1.GatewaySelector {
	return sealsv1.GatewaySelector{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{kubeMetadataNameLabel: ns}},
		PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": app}},
	}
}

// wantGatewayPeer is the apply configuration ingressPeers should build for
// gatewaySel(ns, app).
func wantGatewayPeer(ns, app string) *netv1ac.NetworkPolicyPeerApplyConfiguration {
	return netv1ac.NetworkPolicyPeer().
		WithNamespaceSelector(metav1ac.LabelSelector().
			WithMatchLabels(map[string]string{kubeMetadataNameLabel: ns})).
		WithPodSelector(metav1ac.LabelSelector().
			WithMatchLabels(map[string]string{"app.kubernetes.io/name": app}))
}

// expectedGatewayPeer is wantGatewayPeer as the API object read back from the
// fake client.
func expectedGatewayPeer(ns, app string) netv1.NetworkPolicyPeer {
	return netv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{kubeMetadataNameLabel: ns}},
		PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": app}},
	}
}

// gatewayClass is a class for p whose gateway pods live in kgateway-system.
func gatewayClass(name string, p sealsv1.Protocol) *sealsv1.ExposureClass {
	c := class(name, p)
	sel := gatewaySel("kgateway-system", "kgateway")
	c.Spec.GatewaySelector = &sel

	return &c
}

func TestIngressPeers_ExposureAddsAGatewayPeer(t *testing.T) {
	sels := []sealsv1.GatewaySelector{gatewaySel("kgateway-system", "kgateway")}

	peers, provision := ingressPeers(seal(1), namespace(nil), sels)

	require.True(t, provision, "an exposure is a config, so a policy must be written")
	assert.Equal(t,
		[]*netv1ac.NetworkPolicyPeerApplyConfiguration{wantGatewayPeer("kgateway-system", "kgateway")},
		peers)
}

func TestIngressPeers_GatewayPeerComesAfterTheClaimPeer(t *testing.T) {
	ns := namespace(map[string]string{claimNamespaceAnnotation: "orders"})
	sels := []sealsv1.GatewaySelector{gatewaySel("kgateway-system", "kgateway")}

	peers, provision := ingressPeers(seal(1), ns, sels)

	require.True(t, provision)
	assert.Equal(t, []*netv1ac.NetworkPolicyPeerApplyConfiguration{
		nsPeer("orders"),
		wantGatewayPeer("kgateway-system", "kgateway"),
	}, peers)
}

func TestIngressPeers_GatewayPeerComesAfterAllowedNamespaces(t *testing.T) {
	s := seal(1)
	s.Spec.AllowedNamespaces = []string{"team-a"}
	sels := []sealsv1.GatewaySelector{gatewaySel("kgateway-system", "kgateway")}

	peers, _ := ingressPeers(s, namespace(nil), sels)

	assert.Equal(t, []*netv1ac.NetworkPolicyPeerApplyConfiguration{
		nsPeer("team-a"),
		wantGatewayPeer("kgateway-system", "kgateway"),
	}, peers)
}

func TestIngressPeers_DuplicateSelectorsCollapseToOnePeer(t *testing.T) {
	sels := []sealsv1.GatewaySelector{
		gatewaySel("kgateway-system", "kgateway"),
		gatewaySel("kgateway-system", "kgateway"),
	}

	peers, _ := ingressPeers(seal(1), namespace(nil), sels)

	assert.Len(t, peers, 1, "two exposures on one class must not double the peer")
}

func TestIngressPeers_NoExposuresBehavesAsBefore(t *testing.T) {
	peers, provision := ingressPeers(seal(1), namespace(nil), nil)

	assert.False(t, provision, "an empty seal must still write no policy at all")
	assert.Nil(t, peers)
}

// An empty namespaceSelector matches every namespace, the gateway's included.
// A later change that appends gateway peers here too would look harmless and
// widen nothing, but it would stop allow-all from being one plain peer.
func TestIngressPeers_AllowAllNamespacesStillWinsOverGatewayPeers(t *testing.T) {
	s := seal(1)
	s.Spec.AllowAllNamespaces = ptr.To(true)
	sels := []sealsv1.GatewaySelector{gatewaySel("kgateway-system", "kgateway")}

	peers, provision := ingressPeers(s, namespace(nil), sels)

	require.True(t, provision)
	assert.Equal(t, []*netv1ac.NetworkPolicyPeerApplyConfiguration{
		netv1ac.NetworkPolicyPeer().WithNamespaceSelector(metav1ac.LabelSelector()),
	}, peers)
}

// Class list order is whatever the API server returned. If that leaked into
// the peer order, every reconcile would rewrite the policy.
func TestIngressPeers_PeerOrderDoesNotDependOnInputOrder(t *testing.T) {
	a := gatewaySel("kgateway-system", "kgateway")
	b := gatewaySel("other-system", "other")

	first, _ := ingressPeers(seal(1), namespace(nil), []sealsv1.GatewaySelector{a, b})
	second, _ := ingressPeers(seal(1), namespace(nil), []sealsv1.GatewaySelector{b, a})

	assert.Equal(t, first, second)
}

func TestDedupeSelectors_DropsRepeats(t *testing.T) {
	a := gatewaySel("kgateway-system", "kgateway")
	b := gatewaySel("other-system", "other")

	got := dedupeSelectors([]sealsv1.GatewaySelector{a, b, a})

	assert.Len(t, got, 2)
}

func TestDedupeSelectors_OrderIsStable(t *testing.T) {
	a := gatewaySel("kgateway-system", "kgateway")
	b := gatewaySel("other-system", "other")

	assert.Equal(t,
		dedupeSelectors([]sealsv1.GatewaySelector{a, b}),
		dedupeSelectors([]sealsv1.GatewaySelector{b, a}))
}

// A nil namespaceSelector means "this namespace". An empty one means "every
// namespace". Treating them as the same would drop one of two real peers.
func TestDedupeSelectors_KeepsNilAndEmptyApart(t *testing.T) {
	pods := &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "kgateway"}}
	local := sealsv1.GatewaySelector{PodSelector: pods}
	everywhere := sealsv1.GatewaySelector{NamespaceSelector: &metav1.LabelSelector{}, PodSelector: pods}

	got := dedupeSelectors([]sealsv1.GatewaySelector{local, everywhere})

	assert.Len(t, got, 2)
}

func TestDedupeSelectors_Empty(t *testing.T) {
	assert.Empty(t, dedupeSelectors(nil))
}

func TestLabelSelectorAC_CopiesLabelsAndExpressions(t *testing.T) {
	sel := &metav1.LabelSelector{
		MatchLabels: map[string]string{"app.kubernetes.io/name": "kgateway"},
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      "tier",
			Operator: metav1.LabelSelectorOpIn,
			Values:   []string{"edge", "public"},
		}},
	}

	got := labelSelectorAC(sel)

	assert.Equal(t, metav1ac.LabelSelector().
		WithMatchLabels(map[string]string{"app.kubernetes.io/name": "kgateway"}).
		WithMatchExpressions(metav1ac.LabelSelectorRequirement().
			WithKey("tier").
			WithOperator(metav1.LabelSelectorOpIn).
			WithValues("edge", "public")),
		got)
}

func TestLabelSelectorAC_NilStaysNil(t *testing.T) {
	assert.Nil(t, labelSelectorAC(nil), "an empty selector would match every namespace")
}

func TestLabelSelectorAC_EmptyStaysEmpty(t *testing.T) {
	assert.Equal(t, metav1ac.LabelSelector(), labelSelectorAC(&metav1.LabelSelector{}))
}

// Only a pod selector means gateway pods in the seal's own namespace. The
// peer must not gain a namespaceSelector on the way.
func TestGatewayPeer_OnlyPodSelector(t *testing.T) {
	sel := sealsv1.GatewaySelector{
		PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "kgateway"}},
	}

	got := gatewayPeer(sel)

	require.NotNil(t, got)
	assert.Nil(t, got.NamespaceSelector)
	assert.NotNil(t, got.PodSelector)
}

func tcpExposureOn(className string) sealsv1.Exposure {
	e := tcpExposure("direct", 6379)
	e.ExposureClass = className

	return e
}

func TestGatewaySelectors_TCPExposureUsesItsClass(t *testing.T) {
	classes := []sealsv1.ExposureClass{*gatewayClass("tcp", sealsv1.ProtocolTCP)}

	got, err := gatewaySelectors(classes, []sealsv1.Exposure{tcpExposure("direct", 6379)})

	require.NoError(t, err)
	assert.Equal(t, []sealsv1.GatewaySelector{gatewaySel("kgateway-system", "kgateway")}, got)
}

func TestGatewaySelectors_HTTPExposureAddsNothing(t *testing.T) {
	classes := []sealsv1.ExposureClass{*gatewayClass("http", sealsv1.ProtocolHTTP)}
	web := sealsv1.Exposure{
		Name:     "web",
		Protocol: sealsv1.ProtocolHTTP,
		Backend:  sealsv1.Backend{Service: "redis", Port: 8080},
	}

	got, err := gatewaySelectors(classes, []sealsv1.Exposure{web})

	require.NoError(t, err)
	assert.Empty(t, got, "HTTP is not served yet, so it must not open the policy")
}

func TestGatewaySelectors_ClassWithoutSelectorAddsNothing(t *testing.T) {
	c := class("tcp", sealsv1.ProtocolTCP)

	got, err := gatewaySelectors([]sealsv1.ExposureClass{c}, []sealsv1.Exposure{tcpExposure("direct", 6379)})

	require.NoError(t, err)
	assert.Empty(t, got)
}

// A peer with neither selector is rejected by the API server, and the whole
// policy with it.
func TestGatewaySelectors_SelectorWithBothSidesNilIsSkipped(t *testing.T) {
	c := class("tcp", sealsv1.ProtocolTCP)
	c.Spec.GatewaySelector = &sealsv1.GatewaySelector{}

	got, err := gatewaySelectors([]sealsv1.ExposureClass{c}, []sealsv1.Exposure{tcpExposure("direct", 6379)})

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestGatewaySelectors_MissingClassIsErrClassNotFound(t *testing.T) {
	_, err := gatewaySelectors(nil, []sealsv1.Exposure{tcpExposureOn("deleted")})

	require.Error(t, err)
	assert.True(t, errors.Is(err, errClassNotFound), "the caller must be able to keep the live policy")
}

// A class that exists but does not fit gets no route, so it needs no peer.
// It is not a reason to stop the policy for everything else.
func TestGatewaySelectors_WrongClassIsSkipped(t *testing.T) {
	classes := []sealsv1.ExposureClass{
		*gatewayClass("http", sealsv1.ProtocolHTTP),
		*gatewayClass("tcp", sealsv1.ProtocolTCP),
	}

	got, err := gatewaySelectors(classes, []sealsv1.Exposure{
		tcpExposureOn("http"),
		tcpExposure("admin", 9121),
	})

	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestGatewaySelectors_NoExposures(t *testing.T) {
	got, err := gatewaySelectors([]sealsv1.ExposureClass{*gatewayClass("tcp", sealsv1.ProtocolTCP)}, nil)

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestReconcile_ExposureOpensThePolicyToTheGateway(t *testing.T) {
	s := seal(1)
	s.Spec.Expose = []sealsv1.Exposure{tcpExposure("direct", 6379)}
	m, c := newManager(s, gatewayClass("tcp", sealsv1.ProtocolTCP))

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	assert.Equal(t,
		[]netv1.NetworkPolicyPeer{expectedGatewayPeer("kgateway-system", "kgateway")},
		ingressPeersOf(t, c),
		"no allowedNamespaces and no claim annotation must still let the gateway in")
}

func TestReconcile_HTTPExposureWritesNoPolicy(t *testing.T) {
	s := seal(1)
	s.Spec.Expose = []sealsv1.Exposure{{
		Name:     "web",
		Protocol: sealsv1.ProtocolHTTP,
		Backend:  sealsv1.Backend{Service: "redis", Port: 8080},
	}}
	m, c := newManager(s, gatewayClass("http", sealsv1.ProtocolHTTP))

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	err = c.Get(context.Background(), policyKey(), &netv1.NetworkPolicy{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestReconcile_AllowAllNamespacesWithAnExposureIsOnePeer(t *testing.T) {
	s := seal(1)
	s.Spec.AllowAllNamespaces = ptr.To(true)
	s.Spec.Expose = []sealsv1.Exposure{tcpExposure("direct", 6379)}
	m, c := newManager(s, gatewayClass("tcp", sealsv1.ProtocolTCP))

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	assert.Equal(t, []netv1.NetworkPolicyPeer{allNamespacesPeer}, ingressPeersOf(t, c))
}

// Deleting a class is an operator mistake. It must not cut the traffic of
// every Seal that used it.
func TestReconcile_MissingClassKeepsTheLivePolicy(t *testing.T) {
	s := seal(1)
	s.Spec.Expose = []sealsv1.Exposure{tcpExposure("direct", 6379)}
	live := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: policyKey().Name, Namespace: policyKey().Namespace},
		Spec: netv1.NetworkPolicySpec{
			Ingress: []netv1.NetworkPolicyIngressRule{{
				From: []netv1.NetworkPolicyPeer{expectedGatewayPeer("kgateway-system", "kgateway")},
			}},
		},
	}
	m, c := newManager(s, live)

	_, err := m.Reconcile(context.Background(), ctrl.Request{NamespacedName: sealKey()})
	require.NoError(t, err)

	assert.Equal(t,
		[]netv1.NetworkPolicyPeer{expectedGatewayPeer("kgateway-system", "kgateway")},
		ingressPeersOf(t, c),
		"the gateway peer must survive its class being deleted")

	got := &sealsv1.Seal{}
	require.NoError(t, c.Get(context.Background(), sealKey(), got))
	assert.Equal(t, sealsv1.SealPhaseFailed, got.Status.Phase)
	assert.Contains(t, got.Status.Message, "tcp", "the message must name the missing class")
}
