#!/bin/bash
# apply_example.sh — demonstrates k8said apply with ordered wave deployment
#
# Deploys a realistic 3-tier app in dependency order:
#
#   Wave 1 — infrastructure: namespace, configmap, secret
#   Wave 2 — data:           postgres StatefulSet  (waits until ready)
#   Wave 3 — app:            web Deployment + Service (waits until ready)
#
# Usage:
#   chmod +x apply_example.sh
#   ./apply_example.sh
#
#   # Dry-run (no changes applied):
#   ./apply_example.sh --dry-run
#
#   # AI analysis before applying:
#   ./apply_example.sh --analyze

set -euo pipefail

NAMESPACE="k8said-apply-demo"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
K8SAID="${K8SAID:-$(command -v k8said 2>/dev/null || echo "${SCRIPT_DIR}/../k8said")}"
DRY_RUN=""
ANALYZE=""
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN="--dry-run" ;;
    --analyze) ANALYZE="--analyze" ;;
  esac
done

info()  { echo ""; echo "  [info] $*"; }
ok()    { echo "  [ok]   $*"; }
step()  { echo ""; echo "══════════════════════════════════════════════════════════════"; echo "  $*"; echo "══════════════════════════════════════════════════════════════"; }

# ── preflight ─────────────────────────────────────────────────────────────────
command -v kubectl &>/dev/null || { echo "kubectl not found"; exit 1; }
command -v "$K8SAID" &>/dev/null || { echo "'$K8SAID' not found — run: go install github.com/iasolanki/k8said@latest"; exit 1; }

echo ""
echo "╔══════════════════════════════════════════╗"
echo "║   k8said apply — example script         ║"
echo "╚══════════════════════════════════════════╝"

# ── write manifests to a temp directory ───────────────────────────────────────
TMPDIR=$(mktemp -d /tmp/k8said-apply-example-XXXXXX)
trap 'rm -rf "$TMPDIR"' EXIT

step "Writing manifests to $TMPDIR"

# Wave 1 — infrastructure
cat > "$TMPDIR/namespace.yaml" <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: $NAMESPACE
EOF

cat > "$TMPDIR/configmap.yaml" <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
data:
  DB_HOST: "postgres"
  DB_PORT: "5432"
  DB_NAME: "appdb"
  LOG_LEVEL: "info"
EOF

cat > "$TMPDIR/secret.yaml" <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: app-secret
type: Opaque
stringData:
  DB_USER: "appuser"
  DB_PASSWORD: "s3cr3t"
  POSTGRES_PASSWORD: "s3cr3t"
EOF

# Wave 2 — data (postgres)
cat > "$TMPDIR/postgres.yaml" <<'EOF'
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: postgres
spec:
  serviceName: postgres
  replicas: 1
  selector:
    matchLabels:
      app: postgres
  template:
    metadata:
      labels:
        app: postgres
    spec:
      containers:
      - name: postgres
        image: postgres:15-alpine
        ports:
        - containerPort: 5432
        envFrom:
        - secretRef:
            name: app-secret
        env:
        - name: POSTGRES_DB
          valueFrom:
            configMapKeyRef:
              name: app-config
              key: DB_NAME
        resources:
          requests:
            memory: "128Mi"
            cpu: "100m"
          limits:
            memory: "256Mi"
            cpu: "250m"
        readinessProbe:
          exec:
            command: ["pg_isready", "-U", "appuser", "-d", "appdb"]
          initialDelaySeconds: 5
          periodSeconds: 5
---
apiVersion: v1
kind: Service
metadata:
  name: postgres
spec:
  selector:
    app: postgres
  ports:
  - port: 5432
    targetPort: 5432
  clusterIP: None
EOF

# Wave 3 — app (nginx standing in for a web service)
cat > "$TMPDIR/web-deployment.yaml" <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: 2
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
      - name: web
        image: nginx:1.25-alpine
        ports:
        - containerPort: 80
        envFrom:
        - configMapRef:
            name: app-config
        - secretRef:
            name: app-secret
        resources:
          requests:
            memory: "32Mi"
            cpu: "50m"
          limits:
            memory: "64Mi"
            cpu: "100m"
        readinessProbe:
          httpGet:
            path: /
            port: 80
          initialDelaySeconds: 3
          periodSeconds: 5
EOF

cat > "$TMPDIR/web-service.yaml" <<'EOF'
apiVersion: v1
kind: Service
metadata:
  name: web
spec:
  selector:
    app: web
  ports:
  - port: 80
    targetPort: 80
  type: ClusterIP
EOF

ok "Manifests written."

# ── write the plan file ────────────────────────────────────────────────────────
PLAN="$TMPDIR/plan.yaml"
cat > "$PLAN" <<EOF
namespace: $NAMESPACE
timeout: 120s

waves:
  - name: infrastructure
    manifests:
      - $TMPDIR/namespace.yaml
      - $TMPDIR/configmap.yaml
      - $TMPDIR/secret.yaml

  - name: data
    manifests:
      - $TMPDIR/postgres.yaml

  - name: app
    manifests:
      - $TMPDIR/web-deployment.yaml
      - $TMPDIR/web-service.yaml
EOF

step "Plan"
cat "$PLAN"

# ── run k8said apply ───────────────────────────────────────────────────────────
step "Running k8said apply"

"$K8SAID" apply -f "$PLAN" ${DRY_RUN} ${ANALYZE}

# ── verify (skip on dry-run) ───────────────────────────────────────────────────
if [ -z "$DRY_RUN" ]; then
  step "Verifying"
  echo ""
  kubectl get pods -n "$NAMESPACE"
  echo ""
  kubectl get svc  -n "$NAMESPACE"
fi

# ── cleanup ────────────────────────────────────────────────────────────────────
if [ -z "$DRY_RUN" ]; then
  step "Cleanup"
  info "Deleting namespace $NAMESPACE (this also removes all resources in it)..."
  kubectl delete namespace "$NAMESPACE" --ignore-not-found
  ok "Done."
fi

echo ""
echo "To use your own manifests, write a plan.yaml:"
echo ""
echo "  namespace: my-namespace"
echo "  timeout: 120s"
echo "  waves:"
echo "    - name: infrastructure"
echo "      manifests:"
echo "        - manifests/configmap.yaml"
echo "    - name: data"
echo "      manifests:"
echo "        - manifests/postgres-statefulset.yaml"
echo "    - name: app"
echo "      manifests:"
echo "        - manifests/deployment.yaml"
echo "        - manifests/service.yaml"
echo ""
echo "  k8said apply -f plan.yaml"
echo "  k8said apply -f plan.yaml --dry-run"
echo ""
