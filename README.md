# k8said

[![CI](https://github.com/iasolanki/k8said/actions/workflows/ci.yml/badge.svg)](https://github.com/iasolanki/k8said/actions/workflows/ci.yml)
[![Release](https://github.com/iasolanki/k8said/actions/workflows/release.yml/badge.svg)](https://github.com/iasolanki/k8said/actions/workflows/release.yml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/iasolanki/k8said)](go.mod)
[![pkg.go.dev](https://pkg.go.dev/badge/github.com/iasolanki/k8said.svg)](https://pkg.go.dev/github.com/iasolanki/k8said)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

AI-powered Kubernetes pod diagnosis. Point it at a broken pod, get a plain-English root-cause analysis and fix steps — powered by Claude.

```
$ k8said diagnose crash-loop-5ff7b4885c-jzcn5 --namespace k8s-diagnose

Collecting snapshots...
  • crash-loop-5ff7b4885c-jzcn5 (Running)

────────────────────────────────────────────────────────────
Diagnosing with Claude (streaming)...
────────────────────────────────────────────────────────────

## Pod 1: crash-loop-5ff7b4885c-jzcn5

**Diagnosis:** The container exits with code 1 immediately after startup due to a
missing configuration file. Kubernetes keeps restarting it, producing CrashLoopBackOff.

**Fix:**
1. Check what config the container expects: `kubectl describe pod crash-loop-... -n k8s-diagnose`
2. Create the missing ConfigMap or Secret and mount it at the expected path.
3. Verify locally with `kubectl exec` before redeploying.
```

## What it does

`k8said` collects everything Kubernetes knows about a broken pod — container states, restart history, events, and recent logs — then sends it to Claude (claude-opus-4-7 with adaptive thinking) and streams the diagnosis back to your terminal.

It handles:

| Failure | Example symptom |
|---|---|
| CrashLoopBackOff | Container exits non-zero repeatedly |
| ImagePullBackOff | Wrong image name, tag, or missing registry credentials |
| OOMKilled | Container exceeds its memory limit |
| Pending / Unschedulable | Insufficient cluster resources |
| Missing ConfigMap / Secret | `CreateContainerConfigError` on startup |
| Failing liveness probe | Pod restarted by kubelet with no application crash |
| Init container failure | Main container never starts |

## Requirements

- A running Kubernetes cluster with `kubectl` configured (`~/.kube/config` or `KUBECONFIG`)
- An Anthropic API key in `ANTHROPIC_API_KEY`

## Install

**macOS / Linux — download binary (no Go required):**

```bash
# macOS Apple Silicon
curl -L https://github.com/iasolanki/k8said/releases/latest/download/k8said-darwin-arm64 -o k8said

# macOS Intel
curl -L https://github.com/iasolanki/k8said/releases/latest/download/k8said-darwin-amd64 -o k8said

# Linux amd64
curl -L https://github.com/iasolanki/k8said/releases/latest/download/k8said-linux-amd64 -o k8said
```

Then make it executable and move it to your PATH:

```bash
chmod +x k8said && sudo mv k8said /usr/local/bin/
```

**Go install (requires Go 1.22+):**

```bash
go install github.com/iasolanki/k8said@latest
```

**Build from source:**

```bash
git clone https://github.com/iasolanki/k8said
cd k8said
go build -o k8said .
sudo mv k8said /usr/local/bin/
```

## Usage

### diagnose

```bash
# diagnose a specific pod
k8said diagnose <pod-name> -n <namespace>

# diagnose every broken pod in a namespace
k8said diagnose --all -n <namespace>
```

### preflight

Check for immutable field conflicts before applying:

```bash
k8said preflight -f deploy.yaml -n staging
k8said preflight -f deploy.yaml -n staging --fix
helm template my-release ./chart | k8said preflight -f -
```

### apply

Apply manifests in ordered waves. Write a plan file:

```yaml
# plan.yaml
namespace: staging
timeout: 120s

waves:
  - name: infrastructure
    manifests:
      - manifests/configmap.yaml
      - manifests/secret.yaml

  - name: data
    manifests:
      - manifests/postgres-statefulset.yaml

  - name: app
    manifests:
      - manifests/deployment.yaml
      - manifests/service.yaml
```

Then apply:

```bash
k8said apply -f plan.yaml
k8said apply -f plan.yaml --dry-run

# AI review before applying
k8said apply -f plan.yaml --analyze
```

`--analyze` sends every manifest to Claude before touching the cluster. Claude checks for security issues, missing probes and resource limits, incorrect wave ordering, and obvious misconfigurations, then gives a verdict (**Safe to apply** / **Apply with caution** / **Do not apply**) and prompts you to confirm before proceeding.

Each wave blocks until all Deployments, StatefulSets, DaemonSets, and Jobs in it are healthy before the next wave starts. ConfigMaps, Secrets, Services, and other instant resources proceed immediately.

## Local test cluster

Two helper scripts in `examples/` spin up a disposable minikube cluster pre-loaded with broken workloads:

```bash
# 1. Start a local minikube cluster named "k8said"
./examples/setup_minikube.sh

# 2. Deploy seven intentionally broken pods to the k8s-diagnose namespace
./examples/setup_broken_pods.sh

# 3. Watch them fail
kubectl get pods -n k8s-diagnose -w

# 4. Diagnose one
k8said diagnose --all -n k8s-diagnose
```

Workloads deployed by `examples/setup_broken_pods.sh`:

| Deployment | Failure mode |
|---|---|
| `crash-loop` | Exits 1 after 2s — CrashLoopBackOff |
| `bad-image` | Non-existent image — ImagePullBackOff |
| `oom-killer` | Allocates 50 MB against a 20 Mi limit — OOMKilled |
| `resource-hog` | Requests 500Gi RAM — Pending / Unschedulable |
| `config-missing` | References a ConfigMap that doesn't exist |
| `bad-probe` | nginx with a `/healthz` liveness probe that always 404s |
| `init-fail` | Init container can't reach its dependency and exits 1 |

## Preflight example

`examples/preflight_example.sh` demonstrates two common immutable field conflicts:

```bash
./examples/preflight_example.sh
```

| Scenario | What changes | Why it fails |
|---|---|---|
| StatefulSet | `volumeClaimTemplates` storage 1Gi → 2Gi | PVC spec is immutable after creation |
| Deployment | `selector` label `demo-v1` → `demo-v2` | Selector is immutable after creation |

## Environment variables

| Variable | Description |
|---|---|
| `ANTHROPIC_API_KEY` | Required. Your Anthropic API key. |
| `KUBECONFIG` | Path to kubeconfig. Defaults to `~/.kube/config`. |
