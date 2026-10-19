# Sigillum

**Sigillum**: a seal, the mark an alchemist presses into wax to close a
vessel and show it hasn't been tampered with.

Sigillum handles all network related configurations, so that helmetica managed services are securely accessible.

## Quickstart

Against a kind (or any) cluster:

```bash
kubectl apply -k config/crd
kubectl label namespace default chrysopoeia.io/instance=
just run          # in a second terminal
kubectl apply -k config/samples
kubectl get seals -w   # PHASE -> Ready
kubectl get networkpolicy allow-service-access -o yaml
```

The sample creates a `Seal` named `sample` that allows ingress from
`some-namespace` and asks for one TCP exposure. The controller writes a
`NetworkPolicy` named `allow-service-access` into the seal's namespace and
sets the phase to `Ready` once the exposure is ready.

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

**Keep leader election on when you run more than one replica.** It is on by
default. With `--leader-elect=false`, two replicas hand out the same port to
different exposures.

## Exposures

An entry in `spec.expose` exposes a way into the seal's namespace from
outside the cluster. Only TCP is supported for now. HTTP and HTTPS exposures
are not yet implemented.

```yaml
spec:
  expose:
    - name: service
      protocol: TCP
      backend:
        service: sample
        port: 6379
```

There is no port field: for each exposure, sigillum picks a shard Gateway
with room and a free port from the class's `portRange`, and writes a
ListenerSet and a TCPRoute named `<seal>-<exposure>`. Each exposure is placed
on its own, so exposures of one Seal can land on different shards, and each
one's shard, address, port and URL show up in `status.exposures[]`.

Sigillum never creates Gateways. Before an exposure can be ready, the
platform must provide:

- A Gateway matching the class's `shardSelector`, with `allowedListeners`.
- A gateway controller that serves the listener ports sigillum assigns.
- The backend Service.

## Structure

| Path | What's there |
| ---- | ------------- |
| `api/v1` | The `Seal` and `ExposureClass` types (`seals.helmetica.io/v1`): spec, status and generated deepcopy code. |
| `cmd` | The `sigillum` CLI (cobra): `root.go` wires the binary, `controller.go` is the `sigillum controller` subcommand that builds the manager and registers `SealManager`. |
| `controllers` | `SealManager`, the reconciler for `Seal`. `desiredPhase` writes the `NetworkPolicy`; `ingressPeers` decides who gets in. |
| `controllers/gatewayapi` | Builders and status readers for the Gateway API objects (ListenerSet, TCPRoute, Gateway). The only place their field names are spelled. |
| `config` | Kustomize tree: `crd` (the CRD manifests, installed separately), `rbac`, `manager`, `default` (RBAC + manager, no CRDs) and `samples`. |
