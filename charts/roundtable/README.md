# Round Table Helm Chart

Deploy the entire Knights of the Round Table stack with a single Helm chart.

## Quick Start

```bash
helm dependency update charts/roundtable/
helm install roundtable charts/roundtable/ -n roundtable --create-namespace
```

## Adding Knights

Define knights in `values.yaml` under `knights:`. Each enabled knight gets its own deployment (3-container pod), service, PVC, and configmap.

See [values.yaml](values.yaml) for the full schema and examples.

## Architecture

Each knight pod runs three containers:
- **openclaw** — Agent runtime with personality, memory, and skills
- **nats-bridge** — Sidecar translating NATS ↔ OpenClaw webhook calls
- **git-sync** — Pulls skills from the arsenal repository

Infrastructure (NATS JetStream + Redis) is deployed as subchart dependencies.
