# SKILL: nats-agent-bus

Publish and subscribe to NATS topics for inter-agent communication on the Round Table message bus.

## Scripts

### publish.sh
Publish a message to a NATS topic.

```bash
./scripts/publish.sh <topic> <message>
```

### subscribe.sh
Subscribe to a NATS topic and read messages.

```bash
./scripts/subscribe.sh <topic> [count]
```

## Environment

- `NATS_URL` — NATS server URL (default: `nats://nats.roundtable.svc:4222`)

## Requirements

- `nats` CLI tool installed
