# ⚠️ Deprecated — Use the Helm Chart

This directory contains legacy raw manifests for NATS, Redis, and the namespace.

**The primary deployment method is now the Helm chart at [`charts/roundtable/`](../charts/roundtable/).**

These files are kept as reference only. Use:

```bash
helm dependency update charts/roundtable/
helm install roundtable charts/roundtable/ -n roundtable --create-namespace
```
