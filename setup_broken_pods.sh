#!/bin/bash
# setup_broken_pods.sh — deploys intentionally broken workloads to minikube
# Run this once to populate your cluster with diagnosable problems
#
# Usage:
#   chmod +x setup_broken_pods.sh
#   ./setup_broken_pods.sh

set -e

NAMESPACE="k8s-diagnose"

echo "==> Creating namespace: $NAMESPACE"
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

# ── 1. CrashLoopBackOff ────────────────────────────────────────────────────
echo "==> Deploying crash-loop (CrashLoopBackOff)..."
kubectl apply -n "$NAMESPACE" -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: crash-loop
spec:
  replicas: 1
  selector:
    matchLabels:
      app: crash-loop
  template:
    metadata:
      labels:
        app: crash-loop
    spec:
      containers:
      - name: crasher
        image: busybox:1.36
        command: ["sh", "-c", "echo 'starting...'; sleep 2; echo 'ERROR: config not found' >&2; exit 1"]
        resources:
          limits:
            memory: "32Mi"
            cpu: "50m"
EOF

# ── 2. ImagePullBackOff ────────────────────────────────────────────────────
echo "==> Deploying bad-image (ImagePullBackOff)..."
kubectl apply -n "$NAMESPACE" -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: bad-image
spec:
  replicas: 1
  selector:
    matchLabels:
      app: bad-image
  template:
    metadata:
      labels:
        app: bad-image
    spec:
      containers:
      - name: app
        image: mycompany/nonexistent-app:v99.9.9
        resources:
          limits:
            memory: "64Mi"
            cpu: "50m"
EOF

# ── 3. OOMKilled ───────────────────────────────────────────────────────────
echo "==> Deploying oom-killer (OOMKilled)..."
kubectl apply -n "$NAMESPACE" -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: oom-killer
spec:
  replicas: 1
  selector:
    matchLabels:
      app: oom-killer
  template:
    metadata:
      labels:
        app: oom-killer
    spec:
      containers:
      - name: memory-hog
        image: busybox:1.36
        # Allocates ~50MB but limit is 20Mi — triggers OOMKill
        command: ["sh", "-c", "dd if=/dev/zero bs=1M count=50 | cat > /dev/null"]
        resources:
          limits:
            memory: "20Mi"
            cpu: "100m"
EOF

# ── 4. Pending — insufficient resources ───────────────────────────────────
echo "==> Deploying resource-hog (Pending — unschedulable)..."
kubectl apply -n "$NAMESPACE" -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: resource-hog
spec:
  replicas: 1
  selector:
    matchLabels:
      app: resource-hog
  template:
    metadata:
      labels:
        app: resource-hog
    spec:
      containers:
      - name: app
        image: nginx:1.25
        resources:
          requests:
            memory: "500Gi"
            cpu: "1000"
EOF

# ── 5. Missing ConfigMap ───────────────────────────────────────────────────
echo "==> Deploying config-missing (references non-existent ConfigMap)..."
kubectl apply -n "$NAMESPACE" -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: config-missing
spec:
  replicas: 1
  selector:
    matchLabels:
      app: config-missing
  template:
    metadata:
      labels:
        app: config-missing
    spec:
      containers:
      - name: app
        image: nginx:1.25
        envFrom:
        - configMapRef:
            name: app-config-prod   # this ConfigMap does not exist
        resources:
          limits:
            memory: "64Mi"
            cpu: "50m"
EOF

# ── 6. Failing liveness probe ──────────────────────────────────────────────
echo "==> Deploying bad-probe (failing liveness probe)..."
kubectl apply -n "$NAMESPACE" -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: bad-probe
spec:
  replicas: 1
  selector:
    matchLabels:
      app: bad-probe
  template:
    metadata:
      labels:
        app: bad-probe
    spec:
      containers:
      - name: app
        image: nginx:1.25
        livenessProbe:
          httpGet:
            path: /healthz         # nginx doesn't serve this path → 404 → killed
            port: 80
          initialDelaySeconds: 5
          periodSeconds: 5
          failureThreshold: 2
        resources:
          limits:
            memory: "64Mi"
            cpu: "50m"
EOF

# ── 7. Init container failing ─────────────────────────────────────────────
echo "==> Deploying init-fail (init container exits 1)..."
kubectl apply -n "$NAMESPACE" -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: init-fail
spec:
  replicas: 1
  selector:
    matchLabels:
      app: init-fail
  template:
    metadata:
      labels:
        app: init-fail
    spec:
      initContainers:
      - name: db-check
        image: busybox:1.36
        command: ["sh", "-c", "echo 'Waiting for DB...'; sleep 2; echo 'ERROR: cannot connect to postgres:5432' >&2; exit 1"]
      containers:
      - name: app
        image: nginx:1.25
        resources:
          limits:
            memory: "64Mi"
            cpu: "50m"
EOF

echo ""
echo "==> Done. Broken pods deployed to namespace: $NAMESPACE"
echo ""
echo "Watch pods spin up (and fail):"
echo "  kubectl get pods -n $NAMESPACE -w"
echo ""
echo "Then run the diagnosis:"
echo "  python diagnose.py --namespace $NAMESPACE"
