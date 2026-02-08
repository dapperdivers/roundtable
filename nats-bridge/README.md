# nats-bridge

A lightweight Go sidecar that bridges NATS JetStream messages to OpenClaw webhook endpoints.

## How It Works

1. Connects to NATS and subscribes to configured topics
2. On message received: POSTs the payload to the OpenClaw webhook URL
3. Takes the webhook response and publishes it back to a NATS result topic
4. Exposes `/healthz` for Kubernetes liveness probes

## Configuration

| Environment Variable | Description | Default |
|---------------------|-------------|---------|
| `NATS_URL` | NATS server URL | `nats://nats.roundtable.svc:4222` |
| `SUBSCRIBE_TOPICS` | Comma-separated topics to subscribe | (required) |
| `OPENCLAW_WEBHOOK_URL` | Local OpenClaw webhook endpoint | `http://localhost:18789/webhook` |
| `RESULT_TOPIC_SUFFIX` | Suffix for result topics | `.result` |
| `HEALTH_PORT` | Health check port | `8080` |

## Building

```bash
docker build -t nats-bridge .
```
