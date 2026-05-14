package cmd

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	flag "github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

type applyPlan struct {
	Namespace string      `yaml:"namespace"`
	Timeout   string      `yaml:"timeout"`
	Waves     []applyWave `yaml:"waves"`
}

type applyWave struct {
	Name      string   `yaml:"name"`
	Manifests []string `yaml:"manifests"`
}

var waitableKinds = map[string]string{
	"deployment":  "rollout",
	"statefulset": "rollout",
	"daemonset":   "rollout",
	"job":         "complete",
}

type appliedResource struct {
	kind      string
	name      string
	namespace string
}

// parseApplyOutput extracts kind/name from kubectl apply stdout lines like:
//
//	deployment.apps/myapp created
//	configmap/myapp-config unchanged
func parseApplyOutput(output, defaultNS string) []appliedResource {
	var out []appliedResource
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		parts := strings.Fields(strings.TrimSpace(scanner.Text()))
		if len(parts) < 2 {
			continue
		}
		slash := strings.Index(parts[0], "/")
		if slash < 0 {
			continue
		}
		kind := strings.ToLower(strings.Split(parts[0][:slash], ".")[0])
		name := parts[0][slash+1:]
		out = append(out, appliedResource{kind: kind, name: name, namespace: defaultNS})
	}
	return out
}

func waitForResource(r appliedResource, timeout string) error {
	var args []string
	switch waitableKinds[r.kind] {
	case "rollout":
		args = []string{"rollout", "status", r.kind + "/" + r.name, "--timeout=" + timeout}
	case "complete":
		args = []string{"wait", "job/" + r.name, "--for=condition=complete", "--timeout=" + timeout}
	default:
		return nil
	}
	if r.namespace != "" {
		args = append(args, "-n", r.namespace)
	}
	fmt.Printf("    waiting: kubectl %s\n", strings.Join(args, " "))
	out, err := exec.Command("kubectl", args...).CombinedOutput()
	if len(out) > 0 {
		fmt.Print("    ", strings.TrimSpace(string(out)), "\n")
	}
	if err != nil {
		return fmt.Errorf("%s/%s not ready within %s", r.kind, r.name, timeout)
	}
	return nil
}

func runApply(args []string) {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	planFile := fs.StringP("file", "f", "", "Path to k8said plan YAML")
	dryRun := fs.Bool("dry-run", false, "Print what would be applied without applying")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage:
  k8said apply -f plan.yaml
  k8said apply -f plan.yaml --dry-run

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *planFile == "" {
		fmt.Fprintln(os.Stderr, "error: -f <plan.yaml> is required")
		fs.Usage()
		os.Exit(1)
	}

	data, err := os.ReadFile(*planFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading plan: %v\n", err)
		os.Exit(1)
	}

	var plan applyPlan
	if err := yaml.Unmarshal(data, &plan); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing plan: %v\n", err)
		os.Exit(1)
	}

	if len(plan.Waves) == 0 {
		fmt.Fprintln(os.Stderr, "error: plan contains no waves")
		os.Exit(1)
	}
	if plan.Timeout == "" {
		plan.Timeout = "120s"
	}

	fmt.Printf("\n%s\n", strings.Repeat("─", 60))
	fmt.Printf("  k8said apply — %d wave(s)   timeout: %s", len(plan.Waves), plan.Timeout)
	if plan.Namespace != "" {
		fmt.Printf("   namespace: %s", plan.Namespace)
	}
	if *dryRun {
		fmt.Print("   [dry-run]")
	}
	fmt.Printf("\n%s\n\n", strings.Repeat("─", 60))

	start := time.Now()

	for i, wave := range plan.Waves {
		fmt.Printf("Wave %d/%d — %s\n", i+1, len(plan.Waves), wave.Name)

		var applied []appliedResource

		for _, manifest := range wave.Manifests {
			applyArgs := []string{"apply", "-f", manifest}
			if plan.Namespace != "" {
				applyArgs = append(applyArgs, "-n", plan.Namespace)
			}
			if *dryRun {
				applyArgs = append(applyArgs, "--dry-run=client")
			}
			fmt.Printf("  applying: %s\n", manifest)

			cmd := exec.Command("kubectl", applyArgs...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "\nerror applying %s:\n%s\n", manifest, stderr.String())
				os.Exit(1)
			}

			for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
				if line != "" {
					fmt.Printf("    %s\n", line)
				}
			}
			applied = append(applied, parseApplyOutput(stdout.String(), plan.Namespace)...)
		}

		if !*dryRun {
			waited := false
			for _, r := range applied {
				if _, ok := waitableKinds[r.kind]; !ok {
					continue
				}
				waited = true
				if err := waitForResource(r, plan.Timeout); err != nil {
					fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
					fmt.Fprintf(os.Stderr, "wave %q failed — stopping.\n", wave.Name)
					os.Exit(1)
				}
			}
			if !waited {
				fmt.Println("    (no readiness check needed for this wave)")
			}
		}

		fmt.Printf("  wave %d complete\n\n", i+1)
	}

	fmt.Printf("%s\n", strings.Repeat("─", 60))
	fmt.Printf("All %d wave(s) applied successfully in %s\n",
		len(plan.Waves), time.Since(start).Round(time.Second))
}
