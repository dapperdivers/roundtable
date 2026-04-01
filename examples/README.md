# Examples

Real-world examples of Round Table resources.

| File | Description |
|------|-------------|
| [`knight-basic.yaml`](knight-basic.yaml) | A single knight — security domain with NATS config |
| [`roundtable-fleet.yaml`](roundtable-fleet.yaml) | Full fleet config with warm pool, policies, and NATS streams |
| [`chain-sequential.yaml`](chain-sequential.yaml) | Multi-step chain with output templating between steps |
| [`mission-ephemeral.yaml`](mission-ephemeral.yaml) | Mission using existing fleet knights |
| [`mission-meta.yaml`](mission-meta.yaml) | Meta-mission — planner auto-generates chains and knights |

## Quick Start

```bash
# 1. Install the operator
helm install roundtable oci://ghcr.io/dapperdivers/charts/roundtable-operator \
  --namespace roundtable --create-namespace

# 2. Create a fleet
kubectl apply -f roundtable-fleet.yaml

# 3. Deploy a knight
kubectl apply -f knight-basic.yaml

# 4. Watch it come online
kubectl get knights -n roundtable -w

# 5. Run a chain
kubectl apply -f chain-sequential.yaml

# 6. Launch a mission
kubectl apply -f mission-ephemeral.yaml
kubectl get missions -n roundtable -w
```
