#!/usr/bin/env bash
# Deploy the Round Table operator + a test knight
# Run this from a machine with cluster-admin access
set -euo pipefail

NAMESPACE="roundtable"
REPO="https://raw.githubusercontent.com/dapperdivers/roundtable/main"

echo "🏰 Knights of the Round Table — Deployment"
echo "============================================"
echo ""

# Step 1: CRD
echo "⚔️  Step 1: Installing Knight CRD..."
kubectl apply -f "${REPO}/config/crd/bases/ai.roundtable.io_knights.yaml"
echo "   ✅ CRD installed"
echo ""

# Step 2: Namespace
echo "⚔️  Step 2: Ensuring namespace..."
kubectl get ns ${NAMESPACE} >/dev/null 2>&1 || kubectl create ns ${NAMESPACE}
echo "   ✅ Namespace '${NAMESPACE}' ready"
echo ""

# Step 3: RBAC
echo "⚔️  Step 3: Setting up RBAC..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ServiceAccount
metadata:
  name: roundtable-operator
  namespace: ${NAMESPACE}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: roundtable-operator
rules:
  - apiGroups: ["ai.roundtable.io"]
    resources: ["knights", "knights/status", "knights/finalizers"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["persistentvolumeclaims", "configmaps", "serviceaccounts"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: roundtable-operator
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: roundtable-operator
subjects:
  - kind: ServiceAccount
    name: roundtable-operator
    namespace: ${NAMESPACE}
EOF
echo "   ✅ RBAC configured"
echo ""

# Step 4: Deploy operator
echo "⚔️  Step 4: Deploying operator..."
cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: roundtable-operator
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/name: roundtable-operator
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: roundtable-operator
  template:
    metadata:
      labels:
        app.kubernetes.io/name: roundtable-operator
    spec:
      serviceAccountName: roundtable-operator
      automountServiceAccountToken: true
      containers:
        - name: manager
          image: ghcr.io/dapperdivers/roundtable:latest
          args:
            - --leader-elect
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 200m
              memory: 128Mi
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            runAsNonRoot: true
      terminationGracePeriodSeconds: 10
EOF
echo "   ✅ Operator deployed"
echo ""

# Step 5: Wait for operator
echo "⚔️  Step 5: Waiting for operator to be ready..."
kubectl rollout status deployment/roundtable-operator -n ${NAMESPACE} --timeout=60s
echo "   ✅ Operator is running"
echo ""

# Step 6: Create test knight
echo "⚔️  Step 6: Creating test knight 'sir-test'..."
cat <<EOF | kubectl apply -f -
apiVersion: ai.roundtable.io/v1alpha1
kind: Knight
metadata:
  name: sir-test
  namespace: ${NAMESPACE}
spec:
  domain: testing
  model: claude-haiku-35-20241022
  image: ghcr.io/dapperdivers/pi-knight:latest
  skills:
    - shared
  nats:
    url: nats://nats.database.svc:4222
    subjects:
      - "fleet-a.tasks.testing.>"
    stream: fleet_a_tasks
    resultsStream: fleet_a_results
    maxDeliver: 1
  concurrency: 1
  taskTimeout: 60
  resources:
    memory: 128Mi
    cpu: 100m
EOF
echo "   ✅ Test knight created"
echo ""

# Step 7: Verify
echo "⚔️  Step 7: Checking status..."
sleep 5
echo ""
echo "Knights:"
kubectl get knights -n ${NAMESPACE}
echo ""
echo "Operator logs:"
kubectl logs deployment/roundtable-operator -n ${NAMESPACE} --tail=20
echo ""
echo "Resources created by operator:"
kubectl get deploy,pvc,cm -n ${NAMESPACE} -l app.kubernetes.io/instance=sir-test
echo ""
echo "🏰 Deployment complete! The Round Table awaits."
EOF
