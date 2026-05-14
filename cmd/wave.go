package cmd

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	flag "github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

type wavePlan struct {
	Namespace string     `yaml:"namespace"`
	Timeout   string     `yaml:"timeout"`
	Waves     []waveStep `yaml:"waves"`
}

type waveStep struct {
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

// buildAnalysisPrompt assembles a prompt that includes the full plan structure,
// pre-checked reference findings, and the raw YAML of every manifest.
func buildAnalysisPrompt(plan wavePlan, refIssues []refIssue) string {
	var sb strings.Builder

	sb.WriteString("You are a senior Kubernetes platform engineer reviewing a deployment plan before it is applied to a cluster.\n\n")
	fmt.Fprintf(&sb, "The plan deploys %d wave(s) to namespace %q with a per-resource timeout of %s.\n\n",
		len(plan.Waves), plan.Namespace, plan.Timeout)

	// Inject pre-checked reference findings so Claude knows what was already found.
	sb.WriteString(RefIssueSummary(refIssues))

	sb.WriteString("Analyse the manifests below and report:\n")
	sb.WriteString("1. **Security** — missing security contexts, running as root, privileged containers, host network/PID, overly broad RBAC.\n")
	sb.WriteString("2. **Reliability** — missing readiness/liveness probes, absent resource requests or limits, single replicas for stateless workloads.\n")
	sb.WriteString("3. **Wave ordering** — are dependencies (ConfigMaps, Secrets, Services) applied before the workloads that need them?\n")
	sb.WriteString("4. **Correctness** — obvious misconfigurations (wrong port numbers, bad env var references, missing volume mounts, etc.).\n\n")
	sb.WriteString("For each issue: state the severity (🔴 Critical / 🟡 Warning / 🔵 Info), which resource it affects, and a concrete fix.\n")
	sb.WriteString("End with a one-line summary verdict: **Safe to apply**, **Apply with caution**, or **Do not apply**.\n\n")
	sb.WriteString("---\n\n")

	for i, wave := range plan.Waves {
		fmt.Fprintf(&sb, "## Wave %d: %s\n\n", i+1, wave.Name)
		for _, path := range wave.Manifests {
			fmt.Fprintf(&sb, "### %s\n\n```yaml\n", path)
			content, err := os.ReadFile(path)
			if err != nil {
				fmt.Fprintf(&sb, "# (could not read file: %v)\n", err)
			} else {
				sb.Write(content)
			}
			sb.WriteString("\n```\n\n")
		}
	}

	return sb.String()
}

// streamAnalysis sends the plan to Claude and streams the analysis to stdout.
// Returns false if the user declines to proceed.
func streamAnalysis(ctx context.Context, plan wavePlan, dryRun bool) bool {
	// ── Step 1: deterministic reference check ─────────────────────────────
	fmt.Printf("\n%s\n", strings.Repeat("─", 60))
	fmt.Println("Checking ConfigMap and Secret references...")
	fmt.Printf("%s\n\n", strings.Repeat("─", 60))

	refIssues := CheckRefs(plan)
	FormatRefIssues(refIssues)

	// ── Step 2: Claude analysis ────────────────────────────────────────────
	fmt.Printf("\n%s\n", strings.Repeat("─", 60))
	fmt.Println("Analysing manifests with Claude (streaming)...")
	fmt.Printf("%s\n\n", strings.Repeat("─", 60))

	prompt := buildAnalysisPrompt(plan, refIssues)

	claude := anthropic.NewClient()
	adaptive := anthropic.ThinkingConfigAdaptiveParam{}
	stream := claude.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeOpus4_7,
		MaxTokens: 4096,
		Thinking:  anthropic.ThinkingConfigParamUnion{OfAdaptive: &adaptive},
		System: []anthropic.TextBlockParam{
			{Text: "You are a senior Kubernetes platform engineer. Be concise and actionable. Use markdown formatting."},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})

	inThinking := false
	for stream.Next() {
		event := stream.Current()
		switch ev := event.AsAny().(type) {
		case anthropic.ContentBlockStartEvent:
			switch ev.ContentBlock.AsAny().(type) {
			case anthropic.ThinkingBlock:
				inThinking = true
				fmt.Print("\n[thinking...]\n\n")
			case anthropic.TextBlock:
				if inThinking {
					fmt.Print("\n")
					inThinking = false
				}
			}
		case anthropic.ContentBlockDeltaEvent:
			switch delta := ev.Delta.AsAny().(type) {
			case anthropic.TextDelta:
				fmt.Print(delta.Text)
			}
		}
	}

	if err := stream.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "\nerror from Claude: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n\n%s\n", strings.Repeat("─", 60))

	if dryRun {
		return true
	}

	fmt.Print("Proceed with apply? [Y/n] ")
	var answer string
	_, _ = fmt.Scanln(&answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "" && answer != "y" {
		fmt.Println("Aborted.")
		return false
	}
	return true
}

func runWave(args []string) {
	fs := flag.NewFlagSet("wave", flag.ExitOnError)
	planFile := fs.StringP("file", "f", "", "Path to k8said plan YAML")
	dryRun := fs.Bool("dry-run", false, "Print what would be applied without applying")
	analyze := fs.Bool("analyze", false, "Ask Claude to review manifests before applying")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage:
  k8said wave -f plan.yaml
  k8said wave -f plan.yaml --dry-run
  k8said wave -f plan.yaml --analyze

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

	var plan wavePlan
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

	if *analyze {
		ctx := context.Background()
		if !streamAnalysis(ctx, plan, *dryRun) {
			os.Exit(1)
		}
	}

	fmt.Printf("\n%s\n", strings.Repeat("─", 60))
	fmt.Printf("  k8said wave — %d wave(s)   timeout: %s", len(plan.Waves), plan.Timeout)
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
