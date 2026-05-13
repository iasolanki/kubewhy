package main

import (
	"fmt"
	"os"
)

var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `k8said — AI-powered Kubernetes pod diagnosis

Usage:
  k8said diagnose <pod-name> [flags]   diagnose a specific pod
  k8said diagnose --all     [flags]   diagnose all broken pods in namespace
  k8said preflight -f <manifest>       check for immutable field conflicts before applying

Flags:
  -n, --namespace string   Kubernetes namespace (default "default")
      --all                Scan and diagnose all broken pods

Examples:
  k8said diagnose crash-loop-abc123 --namespace k8s-diagnose
  k8said diagnose --all --namespace k8s-diagnose
  k8said preflight -f deploy.yaml -n staging
  k8said preflight -f deploy.yaml -n staging --fix
  helm template my-release ./chart | k8said preflight -f -
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "diagnose":
		runDiagnose(os.Args[2:])
	case "preflight":
		runPreflight(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("k8said", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}
