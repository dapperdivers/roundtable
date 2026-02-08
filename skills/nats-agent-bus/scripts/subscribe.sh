#!/usr/bin/env bash
set -euo pipefail

NATS_URL="${NATS_URL:-nats://nats.roundtable.svc:4222}"
TOPIC="${1:?Usage: subscribe.sh <topic> [count]}"
COUNT="${2:-1}"

nats sub --server="$NATS_URL" --count="$COUNT" "$TOPIC"
