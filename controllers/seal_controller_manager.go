package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	netv1ac "k8s.io/client-go/applyconfigurations/networking/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	sealsacv1 "github.com/helmetica-framework/sigillum/applyconfiguration/api/v1"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	claimNamespaceAnnotation = "chrysopoeia.io/claim-namespace"
	kubeMetadataNameLabel    = "kubernetes.io/metadata.name"

	fieldOwner = client.FieldOwner("sigillum")

	policyName = "allow-service-access"
)

// SealManager reconciles Seal objects.
type SealManager struct {
	client.Client

	// APIReader bypasses the cache. Port allocation needs it: the cache has
	// not seen the ListenerSet this same controller wrote a moment ago, so a
	// cached list would hand the same port to the next Seal.
	APIReader client.Reader

	// Rand picks ports. Nil means a random seed on first use.
	Rand *rand.Rand

	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	Log      logr.Logger

	// NoGatewayAPI is set when the cluster has no Gateway API CRDs. Exposures
	// then fail and no gateway objects are watched or written.
	NoGatewayAPI bool
}

var errNoGatewayAPI = errors.New("gateway api is not installed in this cluster")

type phase struct {
	Phase     sealsv1.SealPhase
	Message   string
	Exposures []sealsv1.ExposureStatus
}

// +kubebuilder:rbac:groups=seals.helmetica.io,resources=seals,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=seals.helmetica.io,resources=seals/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=seals.helmetica.io,resources=exposureclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=listenersets;tcproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=listenersets/status;tcproutes/status,verbs=get

// Reconcile drives a Seal's status to reflect desiredPhase. There is nothing
// else to do yet: the placeholder owns no other cluster objects, so this is
// just a settle-and-write loop over the Seal itself.
func (r *SealManager) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("seal", req.NamespacedName)

	seal := &sealsv1.Seal{}
	err := r.Get(ctx, req.NamespacedName, seal)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("seal is gone, nothing to do")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !seal.GetDeletionTimestamp().IsZero() {
		log.V(1).Info("seal is being deleted, nothing to do")
		return ctrl.Result{}, nil
	}

	want, rerr := r.desiredPhase(ctx, seal)
	if err := r.writeStatus(ctx, seal, want); err != nil {
		return ctrl.Result{}, errors.Join(rerr, err)
	}
	return ctrl.Result{}, rerr
}

func (r *SealManager) writeStatus(ctx context.Context, seal *sealsv1.Seal, want phase) error {
	if seal.Status.Phase == want.Phase &&
		seal.Status.ObservedGeneration == seal.Generation &&
		seal.Status.Message == want.Message &&
		slices.Equal(seal.Status.Exposures, want.Exposures) {
		return nil
	}

	if seal.Status.Phase != want.Phase {
		log := r.Log.WithValues("seal", client.ObjectKeyFromObject(seal))
		log.Info("seal phase changed", "from", seal.Status.Phase, "to", want.Phase)
	}

	st := sealsacv1.SealStatus().
		WithPhase(want.Phase).
		WithObservedGeneration(seal.Generation).
		WithMessage(want.Message)

	for _, e := range want.Exposures {
		st.WithExposures(exposureStatusAC(e))
	}

	status := sealsacv1.Seal(seal.GetName(), seal.GetNamespace()).WithStatus(st)

	if err := r.Status().Apply(ctx, status, fieldOwner, client.ForceOwnership); err != nil {
		return fmt.Errorf("applying seal status: %w", err)
	}

	return nil
}

// exposureStatusAC sets only the fields that hold a value. Apply sends a set
// zero as a real value, and a port of 0 fails the CRD's minimum of 1.
func exposureStatusAC(e sealsv1.ExposureStatus) *sealsacv1.ExposureStatusApplyConfiguration {
	ac := sealsacv1.ExposureStatus().
		WithName(e.Name).
		WithReady(e.Ready)

	if e.Class != "" {
		ac.WithClass(e.Class)
	}
	if e.Message != "" {
		ac.WithMessage(e.Message)
	}
	if e.Hostname != "" {
		ac.WithHostname(e.Hostname)
	}
	if e.Shard != "" {
		ac.WithShard(e.Shard)
	}
	if e.Address != "" {
		ac.WithAddress(e.Address)
	}
	if e.Port != 0 {
		ac.WithPort(e.Port)
	}
	if e.URL != "" {
		ac.WithURL(e.URL)
	}
	if e.ConflictCount != 0 {
		ac.WithConflictCount(e.ConflictCount)
	}

	return ac
}

// desiredPhase is the seam where sigillum's real logic goes.
func (r *SealManager) desiredPhase(ctx context.Context, seal *sealsv1.Seal) (phase, error) {
	instanceNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: seal.GetNamespace(),
		},
	}

	err := r.Get(ctx, client.ObjectKeyFromObject(instanceNS), instanceNS)
	if err != nil {
		errf := fmt.Errorf("getting namespace: %w", err)
		// Keep the last exposures: an apply without them wipes their ports and
		// conflict counts.
		return phase{Phase: sealsv1.SealPhaseFailed, Message: errf.Error(), Exposures: seal.Status.Exposures}, errf
	}

	var (
		selectors []sealsv1.GatewaySelector
		outcomes  []exposureOutcome
		xerr      error
	)
	if len(seal.Spec.Expose) > 0 {
		classes := &sealsv1.ExposureClassList{}
		if err := r.List(ctx, classes); err != nil {
			errf := fmt.Errorf("listing exposure classes: %w", err)
			return phase{Phase: sealsv1.SealPhaseFailed, Message: errf.Error(), Exposures: seal.Status.Exposures}, errf
		}

		if r.NoGatewayAPI {
			outcomes = noGatewayAPIOutcomes(seal.Spec.Expose)
			return r.applyPolicy(ctx, seal, instanceNS, nil, outcomes)
		}

		outcomes, xerr = r.reconcileExposures(ctx, seal, classes.Items)

		selectors, err = gatewaySelectors(classes.Items, seal.Spec.Expose)
		if errors.Is(err, errClassNotFound) {
			// Leave the live policy alone: rewriting it without this class's
			// peer would cut the gateway off because of an operator mistake.
			return rollUp(outcomes, phase{Phase: sealsv1.SealPhaseFailed, Message: err.Error()}), xerr
		}
		if err != nil {
			return rollUp(outcomes, phase{Phase: sealsv1.SealPhaseFailed, Message: err.Error()}),
				errors.Join(xerr, err)
		}
	} else if !r.NoGatewayAPI {
		// Remove whatever listeners and routes are left. This follows the
		// objects, not status.exposures, so a status that lost its list cannot
		// leak ports.
		if _, err := r.reconcileExposures(ctx, seal, nil); err != nil {
			// Keep the old exposures in status until the cleanup is done.
			return phase{
				Phase:     sealsv1.SealPhaseFailed,
				Message:   err.Error(),
				Exposures: seal.Status.Exposures,
			}, err
		}
	}

	p, err := r.applyPolicy(ctx, seal, instanceNS, selectors, outcomes)
	return p, errors.Join(xerr, err)
}

func noGatewayAPIOutcomes(exposures []sealsv1.Exposure) []exposureOutcome {
	outcomes := make([]exposureOutcome, len(exposures))
	for i, e := range exposures {
		outcomes[i] = exposureOutcome{
			Status: sealsv1.ExposureStatus{Name: e.Name, Message: errNoGatewayAPI.Error()},
			Failed: true,
		}
	}
	return outcomes
}

// applyPolicy writes the seal's NetworkPolicy and rolls outcomes into the
// resulting phase.
func (r *SealManager) applyPolicy(
	ctx context.Context,
	seal *sealsv1.Seal,
	instanceNS *corev1.Namespace,
	selectors []sealsv1.GatewaySelector,
	outcomes []exposureOutcome,
) (phase, error) {
	peers, provision := ingressPeers(seal, instanceNS, selectors)
	if !provision {
		return rollUp(outcomes, phase{Phase: sealsv1.SealPhaseReady, Message: "empty config, skipping provisioning"}), nil
	}

	owner, err := controllerRef(seal, r.Scheme)
	if err != nil {
		errf := fmt.Errorf("building owner reference: %w", err)
		return rollUp(outcomes, phase{Phase: sealsv1.SealPhaseFailed, Message: errf.Error()}), errf
	}

	np := netv1ac.NetworkPolicy(policyName, seal.GetNamespace()).
		WithOwnerReferences(owner).
		WithSpec(netv1ac.NetworkPolicySpec().
			// An empty selector matches every pod, so the policy covers the
			// whole namespace. Apply only sends the fields set here, and
			// podSelector is required, so it has to be spelled out.
			WithPodSelector(metav1ac.LabelSelector()).
			WithIngress(netv1ac.NetworkPolicyIngressRule().WithFrom(peers...)))

	if err := r.Apply(ctx, np, fieldOwner, client.ForceOwnership); err != nil {
		errf := fmt.Errorf("reconciling network policy: %w", err)
		return rollUp(outcomes, phase{Phase: sealsv1.SealPhaseFailed, Message: errf.Error()}), errf
	}

	return rollUp(outcomes, phase{Phase: sealsv1.SealPhaseReady, Message: "networkpolicy applied"}), nil
}

// controllerRef builds the seal's controller owner reference.
// controllerutil.SetControllerReference cannot be used here: it takes a
// metav1.Object, and an apply configuration is not one.
func controllerRef(seal *sealsv1.Seal, scheme *runtime.Scheme) (*metav1ac.OwnerReferenceApplyConfiguration, error) {
	gvk, err := apiutil.GVKForObject(seal, scheme)
	if err != nil {
		return nil, err
	}

	return metav1ac.OwnerReference().
		WithAPIVersion(gvk.GroupVersion().String()).
		WithKind(gvk.Kind).
		WithName(seal.GetName()).
		WithUID(seal.GetUID()).
		WithController(true).
		WithBlockOwnerDeletion(true), nil
}

// ingressPeers works out who may reach this namespace, and reports whether
// there is anything to provision at all. AllowAllNamespaces takes precedence.
//
// gwSelectors come from the ExposureClasses this seal's exposures resolved
// to. Without a peer for the gateway pods, the policy blocks the very traffic
// the exposure exists to allow.
func ingressPeers(
	seal *sealsv1.Seal,
	instanceNS *corev1.Namespace,
	gwSelectors []sealsv1.GatewaySelector,
) ([]*netv1ac.NetworkPolicyPeerApplyConfiguration, bool) {
	if ptr.Deref(seal.Spec.AllowAllNamespaces, false) {
		// A selector that matches no labels matches every namespace.
		return []*netv1ac.NetworkPolicyPeerApplyConfiguration{
			netv1ac.NetworkPolicyPeer().WithNamespaceSelector(metav1ac.LabelSelector()),
		}, true
	}

	peers := []*netv1ac.NetworkPolicyPeerApplyConfiguration{}
	claimNS, ok := instanceNS.GetAnnotations()[claimNamespaceAnnotation]
	if ok {
		peers = append(peers, nsPeer(claimNS))
	}

	// let's not create an allow all by mistake
	if !ok && len(seal.Spec.AllowedNamespaces) == 0 && len(gwSelectors) == 0 {
		return nil, false
	}

	for _, ns := range seal.Spec.AllowedNamespaces {
		peers = append(peers, nsPeer(ns))
	}

	for _, sel := range dedupeSelectors(gwSelectors) {
		peers = append(peers, gatewayPeer(sel))
	}

	return peers, true
}

func nsPeer(ns string) *netv1ac.NetworkPolicyPeerApplyConfiguration {
	return netv1ac.NetworkPolicyPeer().
		WithNamespaceSelector(metav1ac.LabelSelector().
			WithMatchLabels(map[string]string{kubeMetadataNameLabel: ns}))
}

// gatewaySelectors collects the gateway selectors of the classes this seal's
// exposures resolve to.
//
// Only TCP exposures count: HTTP is not served yet, and a peer for a route
// that does not exist opens the namespace for nothing.
//
// A missing class is returned as an error wrapping errClassNotFound. Any other
// resolve error skips that exposure: it gets no route, so it needs no peer.
func gatewaySelectors(classes []sealsv1.ExposureClass, exposures []sealsv1.Exposure) ([]sealsv1.GatewaySelector, error) {
	selectors := make([]sealsv1.GatewaySelector, 0, len(exposures))

	for _, e := range exposures {
		if e.Protocol != sealsv1.ProtocolTCP {
			continue
		}

		ec, err := resolveClass(classes, e)
		if errors.Is(err, errClassNotFound) {
			return nil, fmt.Errorf("collecting gateway selectors: %w", err)
		}
		if err != nil {
			// we skip any other errors
			continue
		}

		sel := ec.Spec.GatewaySelector
		if sel == nil || (sel.NamespaceSelector == nil && sel.PodSelector == nil) {
			continue
		}

		selectors = append(selectors, *sel)
	}

	return selectors, nil
}

// dedupeSelectors drops repeated selectors and sorts the rest, so two
// exposures on one class give one peer, and the policy is not rewritten on
// every reconcile because the order changed.
func dedupeSelectors(sels []sealsv1.GatewaySelector) []sealsv1.GatewaySelector {
	dedupMap := make(map[string]sealsv1.GatewaySelector, len(sels))

	for _, sel := range sels {
		key, _ := json.Marshal(sel)
		dedupMap[string(key)] = sel
	}

	keys := slices.Sorted(maps.Keys(dedupMap))

	deduped := make([]sealsv1.GatewaySelector, 0, len(keys))
	for _, k := range keys {
		deduped = append(deduped, dedupMap[k])
	}

	return deduped
}

// gatewayPeer is the ingress peer for one class's gateway pods.
func gatewayPeer(sel sealsv1.GatewaySelector) *netv1ac.NetworkPolicyPeerApplyConfiguration {
	peer := netv1ac.NetworkPolicyPeer()
	if ns := labelSelectorAC(sel.NamespaceSelector); ns != nil {
		peer.WithNamespaceSelector(ns)
	}
	if pod := labelSelectorAC(sel.PodSelector); pod != nil {
		peer.WithPodSelector(pod)
	}

	return peer
}

// labelSelectorAC turns a label selector into its apply-configuration form.
// A nil selector stays nil: an empty one would match everything.
func labelSelectorAC(sel *metav1.LabelSelector) *metav1ac.LabelSelectorApplyConfiguration {
	if sel == nil {
		return nil
	}

	ac := metav1ac.LabelSelector().WithMatchLabels(sel.MatchLabels)

	for _, r := range sel.MatchExpressions {
		ac.WithMatchExpressions(metav1ac.LabelSelectorRequirement().
			WithKey(r.Key).
			WithOperator(r.Operator).
			WithValues(r.Values...))
	}

	return ac
}

// sealControllerOptions sets one worker, on purpose. Port allocation reads the
// live ListenerSet list and then writes. Two workers interleave those two
// steps and hand out the same port.
func sealControllerOptions() controller.Options {
	return controller.Options{MaxConcurrentReconciles: 1}
}

// SetupWithManager wires the controller: watch Seal, own the objects it writes,
// and reconcile again when an ExposureClass changes.
func (r *SealManager) SetupWithManager(ctx context.Context, name string, mgr ctrl.Manager) error {
	err := mgr.GetFieldIndexer().IndexField(ctx, &sealsv1.Seal{},
		exposureClassIndex, exposureClassIndexKeys)
	if err != nil {
		return err
	}

	b := ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&sealsv1.Seal{}).
		Owns(&netv1.NetworkPolicy{}).
		Watches(&sealsv1.ExposureClass{}, handler.EnqueueRequestsFromMapFunc(r.sealsForClass)).
		WithOptions(sealControllerOptions())

	if !r.NoGatewayAPI {
		b = b.Owns(&gwv1.ListenerSet{}).
			Owns(&gwv1.TCPRoute{}).
			Owns(&gwv1.Gateway{})
	}

	return b.Complete(r)
}
