// Package cmd implements the k8said CLI commands.
package cmd

import (
	"fmt"
	"os"
)

// Version is set at build time via -ldflags.
var Version = "dev"

// Execute is the entry point called from main.
func Execute(version string) {
	Version = version

	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "diagnose":
		runDiagnose(os.Args[2:])
	case "preflight":
		runPreflight(os.Args[2:])
	case "apply":
		runApply(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("k8said", Version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `k8said — AI-powered Kubernetes pod diagnosis and deployment tooling

Usage:
  k8said diagnose <pod-name> [flags]   diagnose a specific pod
  k8said diagnose --all     [flags]   diagnose all broken pods in namespace
  k8said preflight -f <manifest>       check for immutable field conflicts before applying
  k8said apply    -f <plan.yaml>       apply manifests in ordered waves
  k8said apply    -f <plan.yaml> --analyze   AI review before applying

Examples:
  k8said diagnose crash-loop-abc123 --namespace k8s-diagnose
  k8said diagnose --all --namespace k8s-diagnose
  k8said preflight -f deploy.yaml -n staging --fix
  k8said apply -f plan.yaml
  k8said apply -f plan.yaml --dry-run
`)
}
