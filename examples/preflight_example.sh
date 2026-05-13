#!/bin/bash
# preflight_example.sh — demonstrates kubewhy preflight catching immutable field conflicts
#
# This script simulates two common real-world scenarios where kubectl apply / helm upgrade
# would fail silently or get stuck:
#
#   1. StatefulSet volumeClaimTemplates storage size change (immutable)
#   2. Deployment selector label change (immutable)
#
# Usage:
#   chmod +x preflight_example.sh
#   ./preflight_example.sh

set -euo pipefail

NAMESPACE="kubewhy-preflight-demo"
KUBEWHY="${KUBEWHY:-kubewhy}"   # override with KUBEWHY=./kubewhy if not in PATH

info()  { echo ""; echo "  [info] $*"; }
ok()    { echo "  [ok]   $*"; }
step()  { echo ""; echo "══════════════════════════════════════════════════════════════"; echo "  $*"; echo "══════════════════════════════════════════════════════════════"; }

# ── preflight ─────────────────────────────────────────────────────────────────
command -v kubectl &>/dev/null || { echo "kubectl not found"; exit 1; }
command -v "$KUBEWHY" &>/dev/null || { echo "'$KUBEWHY' not found — run: go install github.com/iasolanki/kubewhy@latest"; exit 1; }

echo ""
echo "╔══════════════════════════════════════════╗"
echo "║   kubewhy preflight — example script     ║"
echo "╚══════════════════════════════════════════╝"

kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

# ── Scenario 1: StatefulSet volumeClaimTemplates ──────────────────────────────
step "Scenario 1: StatefulSet — changing volumeClaimTemplates (immutable)"

info "Deploying initial StatefulSet with 1Gi storage..."
kubectl apply -n "$NAMESPACE" -f - <<'EOF'
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: demo-sts
spec:
  serviceName: demo
  replicas: 1
  selector:
    matchLabels:
      app: demo-sts
  template:
    metadata:
      labels:
        app: demo-sts
    spec:
      containers:
      - name: app
        image: nginx:1.25
        resources:
          limits:
            memory: "64Mi"
            cpu: "50m"
        volumeMounts:
        - name: data
          mountPath: /data
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: ["ReadWriteOnce"]
      resources:
        requests:
          storage: 1Gi
EOF
ok "StatefulSet deployed with storage: 1Gi"

# Write the updated manifest (2Gi — immutable change)
UPDATED_STS=$(mktemp /tmp/kubewhy-sts-XXXXXX.yaml)
cat > "$UPDATED_STS" <<'EOF'
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: demo-sts
spec:
  serviceName: demo
  replicas: 1
  selector:
    matchLabels:
      app: demo-sts
  template:
    metadata:
      labels:
        app: demo-sts
    spec:
      containers:
      - name: app
        image: nginx:1.25
        resources:
          limits:
            memory: "64Mi"
            cpu: "50m"
        volumeMounts:
        - name: data
          mountPath: /data
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: ["ReadWriteOnce"]
      resources:
        requests:
          storage: 2Gi   # changed from 1Gi — immutable field
EOF

info "Running preflight check against updated manifest (storage: 1Gi → 2Gi)..."
echo ""
"$KUBEWHY" preflight -f "$UPDATED_STS" -n "$NAMESPACE" || true
rm -f "$UPDATED_STS"

# ── Scenario 2: Deployment selector label change ──────────────────────────────
step "Scenario 2: Deployment — changing selector labels (immutable)"

info "Deploying initial Deployment with selector app=demo-v1..."
kubectl apply -n "$NAMESPACE" -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo-deploy
spec:
  replicas: 1
  selector:
    matchLabels:
      app: demo-v1
  template:
    metadata:
      labels:
        app: demo-v1
    spec:
      containers:
      - name: app
        image: nginx:1.25
        resources:
          limits:
            memory: "64Mi"
            cpu: "50m"
EOF
ok "Deployment deployed with selector app=demo-v1"

UPDATED_DEPLOY=$(mktemp /tmp/kubewhy-deploy-XXXXXX.yaml)
cat > "$UPDATED_DEPLOY" <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo-deploy
spec:
  replicas: 1
  selector:
    matchLabels:
      app: demo-v2   # changed from demo-v1 — selector is immutable
  template:
    metadata:
      labels:
        app: demo-v2
    spec:
      containers:
      - name: app
        image: nginx:1.25
        resources:
          limits:
            memory: "64Mi"
            cpu: "50m"
EOF

info "Running preflight check against updated manifest (selector: demo-v1 → demo-v2)..."
echo ""
"$KUBEWHY" preflight -f "$UPDATED_DEPLOY" -n "$NAMESPACE" || true
rm -f "$UPDATED_DEPLOY"

# ── cleanup ───────────────────────────────────────────────────────────────────
step "Cleanup"
info "Deleting namespace $NAMESPACE..."
kubectl delete namespace "$NAMESPACE" --ignore-not-found
ok "Done."

echo ""
echo "To use --fix and let kubewhy delete and re-apply automatically:"
echo "  kubewhy preflight -f your-manifest.yaml -n your-namespace --fix"
echo ""
echo "Works with helm too:"
echo "  helm template my-release ./chart | kubewhy preflight -f - -n your-namespace"
echo ""
