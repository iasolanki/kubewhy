#!/bin/bash
# setup_minikube.sh — start a local minikube cluster ready for kubewhy
#
# Usage:
#   chmod +x setup_minikube.sh
#   ./setup_minikube.sh

set -euo pipefail

CLUSTER_NAME="kubewhy"
CPUS=2
MEMORY="4096"   # MB
DRIVER=""       # auto-detect below

# ── helpers ───────────────────────────────────────────────────────────────
info()  { echo "  [info] $*"; }
ok()    { echo "  [ok]   $*"; }
die()   { echo "  [err]  $*" >&2; exit 1; }

require() {
  command -v "$1" &>/dev/null || die "'$1' not found. Install it first."
}

# ── preflight ─────────────────────────────────────────────────────────────
echo ""
echo "╔══════════════════════════════════════════╗"
echo "║   kubewhy — minikube cluster setup       ║"
echo "╚══════════════════════════════════════════╝"
echo ""

require minikube
require kubectl

# Pick a driver: docker > orbstack > hyperkit > virtualbox > qemu
pick_driver() {
  for d in docker orbstack hyperkit virtualbox qemu2; do
    if command -v "$d" &>/dev/null 2>&1; then
      echo "$d"
      return
    fi
  done
  # docker is the safest cross-platform default
  echo "docker"
}

DRIVER=$(pick_driver)
info "Using driver: $DRIVER"

# ── check if cluster already exists ───────────────────────────────────────
if minikube status -p "$CLUSTER_NAME" &>/dev/null 2>&1; then
  CURRENT_STATUS=$(minikube status -p "$CLUSTER_NAME" --format='{{.Host}}' 2>/dev/null || echo "Unknown")
  if [ "$CURRENT_STATUS" = "Running" ]; then
    ok "Cluster '$CLUSTER_NAME' is already running."
    minikube status -p "$CLUSTER_NAME"
    echo ""
    info "Switching kubectl context..."
    minikube update-context -p "$CLUSTER_NAME"
    ok "kubectl context → $CLUSTER_NAME"
    echo ""
    echo "Ready. Skip to:"
    echo "  ./setup_broken_pods.sh"
    exit 0
  else
    info "Cluster '$CLUSTER_NAME' exists but is stopped — starting it..."
    minikube start -p "$CLUSTER_NAME"
    minikube update-context -p "$CLUSTER_NAME"
    ok "Cluster started."
    exit 0
  fi
fi

# ── start fresh cluster ───────────────────────────────────────────────────
echo ""
info "Starting new minikube cluster: $CLUSTER_NAME"
info "  cpus=$CPUS  memory=${MEMORY}MB  driver=$DRIVER"
echo ""

minikube start \
  --profile="$CLUSTER_NAME" \
  --driver="$DRIVER" \
  --cpus="$CPUS" \
  --memory="$MEMORY" \
  --kubernetes-version=stable \
  --wait=all

echo ""
info "Switching kubectl context to '$CLUSTER_NAME'..."
minikube update-context -p "$CLUSTER_NAME"

# ── verify ────────────────────────────────────────────────────────────────
echo ""
info "Cluster info:"
kubectl cluster-info

echo ""
info "Nodes:"
kubectl get nodes -o wide

echo ""
ok "Cluster '$CLUSTER_NAME' is ready."
echo ""
echo "Next steps:"
echo "  1. Deploy broken workloads:  ./setup_broken_pods.sh"
echo "  2. Watch pods fail:          kubectl get pods -n k8s-diagnose -w"
echo "  3. Run diagnosis:            python diagnose.py --namespace k8s-diagnose"
echo ""
echo "To stop the cluster later:"
echo "  minikube stop -p $CLUSTER_NAME"
echo ""
echo "To delete it entirely:"
echo "  minikube delete -p $CLUSTER_NAME"
