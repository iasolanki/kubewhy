# kubewhy

AI-powered Kubernetes pod diagnosis. Point it at a broken pod, get a plain-English root-cause analysis and fix steps — powered by Claude.

```
$ kubewhy diagnose crash-loop-5ff7b4885c-jzcn5 --namespace k8s-diagnose

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

`kubewhy` collects everything Kubernetes knows about a broken pod — container states, restart history, events, and recent logs — then sends it to Claude (claude-opus-4-7 with adaptive thinking) and streams the diagnosis back to your terminal.

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

- Go 1.22+
- A running Kubernetes cluster with `kubectl` configured (`~/.kube/config` or `KUBECONFIG`)
- An Anthropic API key in `ANTHROPIC_API_KEY`

## Install

```bash
git clone https://github.com/yourname/kubewhy
cd kubewhy
go build -o kubewhy .
sudo mv kubewhy /usr/local/bin/
```

## Usage

```bash
# diagnose a specific pod
kubewhy diagnose <pod-name> --namespace <namespace>
kubewhy diagnose <pod-name> -n <namespace>

# diagnose every broken pod in a namespace
kubewhy diagnose --all --namespace <namespace>
```

The pod name can be a full name from `kubectl get pods`:

```bash
kubectl get pods -n k8s-diagnose
# NAME                            READY   STATUS             RESTARTS
# crash-loop-5ff7b4885c-jzcn5    0/1     CrashLoopBackOff   7

kubewhy diagnose crash-loop-5ff7b4885c-jzcn5 -n k8s-diagnose
```

## Local test cluster

Two helper scripts spin up a disposable minikube cluster pre-loaded with broken workloads:

```bash
# 1. Start a local minikube cluster named "kubewhy"
./setup_minikube.sh

# 2. Deploy seven intentionally broken pods to the k8s-diagnose namespace
./setup_broken_pods.sh

# 3. Watch them fail
kubectl get pods -n k8s-diagnose -w

# 4. Diagnose one
kubewhy diagnose --all -n k8s-diagnose
```

Workloads deployed by `setup_broken_pods.sh`:

| Deployment | Failure mode |
|---|---|
| `crash-loop` | Exits 1 after 2s — CrashLoopBackOff |
| `bad-image` | Non-existent image — ImagePullBackOff |
| `oom-killer` | Allocates 50 MB against a 20 Mi limit — OOMKilled |
| `resource-hog` | Requests 500Gi RAM — Pending / Unschedulable |
| `config-missing` | References a ConfigMap that doesn't exist |
| `bad-probe` | nginx with a `/healthz` liveness probe that always 404s |
| `init-fail` | Init container can't reach its dependency and exits 1 |

## Environment variables

| Variable | Description |
|---|---|
| `ANTHROPIC_API_KEY` | Required. Your Anthropic API key. |
| `KUBECONFIG` | Path to kubeconfig. Defaults to `~/.kube/config`. |
