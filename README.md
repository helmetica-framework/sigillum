# Sigillum

**Sigillum**: a network config, the mark an alchemist presses into wax to close a vessel and show it hasn't been tampered with.

Sigillum handles all network related configurations, so that helmetica managed services are securely accessible: network policies from a `NetworkConfig`, and exposure outside the cluster through `ExposureClass`es.

Documentation: https://docs.helmetica.io/sigillum/

## Development

```bash
just test
just run    # against the cluster in your KUBECONFIG
just docs   # preview the docs
```
