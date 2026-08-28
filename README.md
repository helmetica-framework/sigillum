# Sigillum

**Sigillum**: a seal, the mark an alchemist presses into wax to close a
vessel and show it hasn't been tampered with.

Sigillum handles all network related configurations, so that helmetica managed services are securely accessible.

## Quickstart

Against a kind (or any) cluster:

```bash
kubectl apply -k config/crd
just run          # in a second terminal
kubectl apply -k config/samples
kubectl get seals -w   # PHASE -> Ready
kubectl get networkpolicy allow-service-access -o yaml
```

The sample creates a `Seal` named `sample` that allows ingress from
`some-namespace`. The controller writes a `NetworkPolicy` named
`allow-service-access` into the seal's namespace and sets the phase to
`Ready`.

Ingress peers come from three places:

| Source | Effect |
| ------ | ------ |
| The `chrysopoeia.io/claim-namespace` annotation on the seal's own namespace | Adds a peer for the claim namespace. |
| `spec.allowedNamespaces` | Adds a peer per named namespace. |
| `spec.allowAllNamespaces: true` | A single `namespaceSelector: {}` peer, matching every namespace in the cluster. Overrides the other two. Traffic from outside the cluster stays blocked. |

With no annotation and an empty `allowedNamespaces`, no policy is written
at all, so an incomplete spec can't turn into an accidental allow-all. Set
`allowAllNamespaces` when you actually want one.

**`config/default` deploys RBAC and the manager, but not the CRDs.** Install
the CRDs separately from `config/crd` first:

```bash
kubectl apply -k config/crd
kubectl apply -k config/default
```

## Structure

| Path | What's there |
| ---- | ------------- |
| `api/v1` | The `Seal` type (`seals.helmetica.io/v1`): spec, status and generated deepcopy code. |
| `cmd` | The `sigillum` CLI (cobra): `root.go` wires the binary, `controller.go` is the `sigillum controller` subcommand that builds the manager and registers `SealManager`. |
| `controllers` | `SealManager`, the reconciler for `Seal`. `desiredPhase` writes the `NetworkPolicy`; `ingressPeers` decides who gets in. |
| `config` | Kustomize tree: `crd` (the CRD manifest, installed separately), `rbac`, `manager`, `default` (RBAC + manager, no CRDs) and `samples`. |
