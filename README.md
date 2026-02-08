# Knights of the Round Table 🏰⚔️

> A multi-agent AI platform for Kubernetes homelabs

## Overview

The Round Table is an orchestration platform where specialized AI agents ("Knights") collaborate through a NATS JetStream message bus, deployed via GitOps on Kubernetes.

Each Knight is an [OpenClaw](https://github.com/anthropics/anthropic-cookbook) AI gateway with a unique personality, skillset, and area of responsibility. They communicate peer-to-peer through NATS topics, coordinated by a central enchanter.

## Architecture

```
Derek (Human Lord) ──→ Tim the Enchanter (Lead Agent)
                            │
                      NATS JetStream
                       ┌────┼────┐
                       ▼    ▼    ▼
                   Galahad  ...  Future Knights
                  (Security)

Drake (Derek's Son) ──→ Munin (Drake's Agent / Tim's Apprentice)
```

### The Hierarchy

- **Derek** → Commands **Tim the Enchanter**, the lead AI agent
- **Drake** → Commands **Munin**, Tim's apprentice (a raven)
- **Knights** → Serve Tim. They receive tasks via NATS, execute autonomously, and report back
- Knights **never** interact with humans directly — Tim is the interface

### How It Works

1. Tim receives a request (or decides proactively) that needs a specialist
2. Tim publishes a message to a NATS topic (e.g., `knight.galahad.task`)
3. The Knight's **nats-bridge sidecar** receives the message and POSTs it to the local OpenClaw webhook
4. The Knight processes the task and responds via NATS
5. Tim receives the result and acts on it

## Components

| Component | Description |
|-----------|-------------|
| **nats-bridge** | Go sidecar that bridges NATS ↔ OpenClaw webhooks |
| **infrastructure/** | Flux HelmReleases for NATS, Redis, namespaces |
| **knights/** | Kustomize-based Knight deployments |
| **skills/** | OpenClaw skills shared across knights |

## Quick Start

> 🚧 TODO — Coming in Phase 2

## Roadmap

### Phase 1: Foundation ✅
- [x] Project scaffold
- [ ] NATS JetStream deployment
- [ ] nats-bridge sidecar (build + deploy)
- [ ] Knight template finalized

### Phase 2: First Knight
- [ ] Galahad (Security) fully operational
- [ ] Tim ↔ Galahad communication verified
- [ ] Task/response message contract defined

### Phase 3: Expansion
- [ ] Additional knights (Merlin, Percival, etc.)
- [ ] Redis state store integration
- [ ] Cross-knight collaboration patterns

### Phase 4: Intelligence
- [ ] Proactive knight behaviors
- [ ] Event-driven triggers (webhooks, cron, alerts)
- [ ] Munin ↔ Knight communication

## License

MIT — see [LICENSE](LICENSE)
