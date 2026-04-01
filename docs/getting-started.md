# Getting Started

Deploy your first AI agent fleet in 5 minutes.

## Prerequisites

- Kubernetes cluster (1.28+)
- Helm 3.12+
- NATS JetStream running in-cluster
- An LLM API key (Anthropic, OpenAI, etc.)

## 1. Install the Operator

```bash
helm install roundtable oci://ghcr.io/dapperdivers/charts/roundtable-operator \
  --namespace roundtable --create-namespace \
  --set images.piKnight.tag=latest
```

## 2. Create a NATS Secret

```bash
kubectl create secret generic llm-api-keys \
  --namespace roundtable \
  --from-literal=ANTHROPIC_API_KEY=sk-ant-...
```

## 3. Deploy a RoundTable

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: RoundTable
metadata:
  name: my-fleet
  namespace: roundtable
spec:
  nats:
    url: nats://nats.database.svc:4222
    subjectPrefix: my-fleet
    tasksStream: my_fleet_tasks
    resultsStream: my_fleet_results
    createStreams: true
  defaults:
    model: claude-sonnet-4-20250514
    concurrency: 2
    taskTimeout: 120
```

```bash
kubectl apply -f roundtable.yaml
kubectl get rt -n roundtable
```

## 4. Deploy Your First Knight

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: Knight
metadata:
  name: my-first-knight
  namespace: roundtable
spec:
  domain: general
  model: claude-sonnet-4-20250514
  skills:
    - general
  nats:
    url: nats://nats.database.svc:4222
    stream: my_fleet_tasks
    resultsStream: my_fleet_results
    subjects:
      - "my-fleet.tasks.general.>"
```

```bash
kubectl apply -f knight.yaml
kubectl get knights -n roundtable -w
# Wait for Phase: Ready
```

## 5. Send a Task

```bash
# Install NATS CLI: https://github.com/nats-io/natscli
nats pub my-fleet.tasks.general.my-first-knight '{
  "task_id": "test-1",
  "task": "What is Kubernetes? Explain in 2 sentences."
}' --server nats://nats.database.svc:4222

# Watch for results
nats sub "my-fleet.results.>" --server nats://nats.database.svc:4222
```

## 6. Create a Chain

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: Chain
metadata:
  name: hello-chain
  namespace: roundtable
spec:
  timeout: 600
  input: "Tell me about Kubernetes operators"
  steps:
    - name: research
      knightRef: my-first-knight
      prompt: "Research this topic: {{ .Input }}"
    - name: summarize
      knightRef: my-first-knight
      prompt: "Summarize this in 3 bullet points: {{ .Steps.research.Output }}"
```

```bash
kubectl apply -f chain.yaml
kubectl get chains -n roundtable -w
```

## What's Next?

- [Example Manifests](../examples/) — Real-world Knight, Chain, Mission examples
- [Helm Values Reference](helm-values.md) — Full configuration options
- [Architecture Overview](ARCHITECTURE.md) — How it all works under the hood
- [API Reference](api-reference.md) — Complete CRD field documentation
