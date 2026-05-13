# Contributing to k8said

Thanks for your interest. This document covers how to set up a dev environment, run the examples, and submit a PR.

## Requirements

- Go 1.22+
- `kubectl` configured against a cluster (minikube works fine)
- An Anthropic API key in `ANTHROPIC_API_KEY` (for `diagnose` only)
- `golangci-lint` for linting (`brew install golangci-lint` on macOS)

## Local setup

```bash
git clone https://github.com/iasolanki/k8said
cd k8said
make build          # produces ./k8said binary
./k8said --help
```

## Spin up a test cluster

```bash
./setup_minikube.sh              # starts a local minikube cluster named "k8said"
./examples/setup_broken_pods.sh  # deploys seven broken workloads to k8s-diagnose namespace
```

## Running the examples

```bash
# diagnose a specific broken pod
./k8said diagnose <pod-name> -n k8s-diagnose

# diagnose all broken pods
./k8said diagnose --all -n k8s-diagnose

# check a manifest for immutable field conflicts
./examples/preflight_example.sh
```

## Before submitting a PR

```bash
make vet    # must pass
make lint   # must pass
make build  # must produce a working binary
```

Test your change against the minikube cluster above and update the README or examples if you're adding a command or flag.

## Commit style

```
<type>: <short summary>

# types: feat, fix, docs, refactor, chore
```

Examples:
- `feat: add --output=json flag to diagnose`
- `fix: handle pods with no events gracefully`
- `docs: add preflight usage to README`

## Opening a PR

Fill in the PR template. Link any related issue with `Closes #N`.
