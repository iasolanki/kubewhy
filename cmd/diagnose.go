package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/iasolanki/k8said/pkg/k8s"
	flag "github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func buildPrompt(snaps []k8s.PodSnapshot) string {
	var sb strings.Builder
	sb.WriteString("You are diagnosing broken Kubernetes pods. For each pod below, identify the root cause and provide specific remediation steps.\n\n")

	for i, s := range snaps {
		fmt.Fprintf(&sb, "## Pod %d: %s (namespace: %s)\n", i+1, s.Name, s.Namespace)
		fmt.Fprintf(&sb, "Phase: %s\n\n", s.Phase)

		if len(s.Conditions) > 0 {
			sb.WriteString("### Conditions\n")
			for _, c := range s.Conditions {
				line := fmt.Sprintf("- %s=%s", c.Type, c.Status)
				if c.Reason != "" {
					line += fmt.Sprintf(" [%s]", c.Reason)
				}
				if c.Message != "" {
					line += fmt.Sprintf(": %s", c.Message)
				}
				sb.WriteString(line + "\n")
			}
			sb.WriteString("\n")
		}

		printContainers := func(label string, containers []k8s.ContainerSnap) {
			if len(containers) == 0 {
				return
			}
			fmt.Fprintf(&sb, "### %s\n", label)
			for _, c := range containers {
				fmt.Fprintf(&sb, "- **%s** (image: %s, restarts: %d, ready: %v)\n",
					c.Name, c.Image, c.RestartCount, c.Ready)
				if c.State.Reason != "" {
					fmt.Fprintf(&sb, "  State: %s", c.State.Reason)
					if c.State.Message != "" {
						fmt.Fprintf(&sb, " — %s", c.State.Message)
					}
					if c.State.ExitCode != 0 {
						fmt.Fprintf(&sb, " (exit %d)", c.State.ExitCode)
					}
					sb.WriteString("\n")
				}
				if c.LastState.Reason != "" {
					fmt.Fprintf(&sb, "  Last: %s (exit %d)\n", c.LastState.Reason, c.LastState.ExitCode)
				}
			}
			sb.WriteString("\n")
		}
		printContainers("Init Containers", s.InitContainers)
		printContainers("Containers", s.Containers)

		if len(s.Events) > 0 {
			sb.WriteString("### Recent Events\n")
			for _, e := range s.Events {
				fmt.Fprintf(&sb, "- [%s] %s (×%d, %s ago)\n", e.Reason, e.Message, e.Count, e.Age)
			}
			sb.WriteString("\n")
		}

		if len(s.Logs) > 0 {
			sb.WriteString("### Logs\n")
			for key, text := range s.Logs {
				fmt.Fprintf(&sb, "**%s:**\n```\n%s\n```\n\n", key, text)
			}
		}

		sb.WriteString("---\n\n")
	}

	sb.WriteString("For each pod: state the diagnosis (1-2 sentences), then list concrete fix steps.\n")
	return sb.String()
}

func runDiagnose(args []string) {
	fs := flag.NewFlagSet("diagnose", flag.ExitOnError)
	ns := fs.StringP("namespace", "n", "default", "Kubernetes namespace")
	all := fs.Bool("all", false, "Diagnose all broken pods in the namespace")
	fs.Usage = usage
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	podName := fs.Arg(0)
	if podName == "" && !*all {
		fmt.Fprintf(os.Stderr, "error: specify a pod name or use --all\n\n")
		usage()
		os.Exit(1)
	}

	ctx := context.Background()

	clientset, err := k8s.BuildClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var targets []corev1.Pod

	if *all {
		pods, err := clientset.CoreV1().Pods(*ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error listing pods: %v\n", err)
			os.Exit(1)
		}
		for _, p := range pods.Items {
			if k8s.IsBroken(p) {
				targets = append(targets, p)
			}
		}
		if len(targets) == 0 {
			fmt.Printf("No broken pods found in namespace %q.\n", *ns)
			return
		}
		fmt.Printf("Found %d broken pod(s) in namespace %q\n\n", len(targets), *ns)
	} else {
		pod, err := clientset.CoreV1().Pods(*ns).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: pod %q not found in namespace %q: %v\n", podName, *ns, err)
			os.Exit(1)
		}
		targets = []corev1.Pod{*pod}
	}

	fmt.Println("Collecting snapshots...")
	var snaps []k8s.PodSnapshot
	for _, p := range targets {
		fmt.Printf("  • %s (%s)\n", p.Name, p.Status.Phase)
		snaps = append(snaps, k8s.CollectSnapshot(ctx, clientset, p))
	}

	prompt := buildPrompt(snaps)

	fmt.Printf("\n%s\n", strings.Repeat("─", 60))
	fmt.Println("Diagnosing with Claude (streaming)...")
	fmt.Printf("%s\n\n", strings.Repeat("─", 60))

	claude := anthropic.NewClient()

	adaptive := anthropic.ThinkingConfigAdaptiveParam{}
	stream := claude.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeOpus4_7,
		MaxTokens: 4096,
		Thinking:  anthropic.ThinkingConfigParamUnion{OfAdaptive: &adaptive},
		System: []anthropic.TextBlockParam{
			{Text: "You are a senior Kubernetes reliability engineer. Diagnose pod failures clearly and concisely, focusing on actionable fixes."},
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
	fmt.Println("Diagnosis complete.")
}
