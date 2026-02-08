#!/usr/bin/env bash
set -euo pipefail

NATS_URL="${NATS_URL:-nats://nats.roundtable.svc:4222}"
TOPIC="${1:?Usage: publish.sh <topic> <message>}"
MESSAGE="${2:?Usage: publish.sh <topic> <message>}"

nats pub --server="$NATS_URL" "$TOPIC" "$MESSAGE"
