package controllers

import (
	"context"
	"fmt"

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

	sealsv1 "github.com/helmetica-framework/sigillum/api/v1"
	sealsacv1 "github.com/helmetica-framework/sigillum/applyconfiguration/api/v1"
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
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	Log      logr.Logger
}

type phase struct {
	Phase   sealsv1.SealPhase
	Message string
}

// +kubebuilder:rbac:groups=seals.helmetica.io,resources=seals,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=seals.helmetica.io,resources=seals/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

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

	want, err := r.desiredPhase(ctx, seal)
	if err != nil {
		return ctrl.Result{}, err
	}

	if seal.Status.Phase == want.Phase &&
		seal.Status.ObservedGeneration == seal.Generation &&
		seal.Status.Message == want.Message {
		return ctrl.Result{}, nil
	}

	if seal.Status.Phase != want.Phase {
		log.Info("seal phase changed", "from", seal.Status.Phase, "to", want.Phase)
	}

	status := sealsacv1.Seal(seal.GetName(), seal.GetNamespace()).
		WithStatus(sealsacv1.SealStatus().
			WithPhase(want.Phase).
			WithObservedGeneration(seal.Generation).
			WithMessage(want.Message))

	if err := r.Status().Apply(ctx, status, fieldOwner, client.ForceOwnership); err != nil {
		return ctrl.Result{}, fmt.Errorf("applying seal status: %w", err)
	}
	return ctrl.Result{}, nil
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
		return phase{Phase: sealsv1.SealPhaseFailed, Message: errf.Error()}, errf
	}

	peers, provision := ingressPeers(seal, instanceNS)
	if !provision {
		return phase{Phase: sealsv1.SealPhaseReady, Message: "empty config, skipping provisioning"}, nil
	}

	owner, err := controllerRef(seal, r.Scheme)
	if err != nil {
		errf := fmt.Errorf("building owner reference: %w", err)
		return phase{Phase: sealsv1.SealPhaseFailed, Message: errf.Error()}, errf
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
		return phase{Phase: sealsv1.SealPhaseFailed, Message: errf.Error()}, errf
	}

	return phase{Phase: sealsv1.SealPhaseReady, Message: "networkpolicy applied"}, nil
}

// controllerRef builds the seal's controller owner reference as an apply
// configuration. controllerutil.SetControllerReference cannot be used here:
// it takes a metav1.Object, and an apply configuration is not one.
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
func ingressPeers(seal *sealsv1.Seal, instanceNS *corev1.Namespace) ([]*netv1ac.NetworkPolicyPeerApplyConfiguration, bool) {
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
	if !ok && len(seal.Spec.AllowedNamespaces) == 0 {
		return nil, false
	}

	for _, ns := range seal.Spec.AllowedNamespaces {
		peers = append(peers, nsPeer(ns))
	}

	return peers, true
}

func nsPeer(ns string) *netv1ac.NetworkPolicyPeerApplyConfiguration {
	return netv1ac.NetworkPolicyPeer().
		WithNamespaceSelector(metav1ac.LabelSelector().
			WithMatchLabels(map[string]string{kubeMetadataNameLabel: ns}))
}

// SetupWithManager wires the controller: watch Seal. Nothing else is owned
// yet, so there are no additional watches.
func (r *SealManager) SetupWithManager(name string, mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&sealsv1.Seal{}).
		Owns(&netv1.NetworkPolicy{}).
		Complete(r)
}
