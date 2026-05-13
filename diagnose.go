// diagnose.go — AI-powered Kubernetes pod diagnosis
//
// Usage:
//   go build -o kubewhy .
//   kubewhy diagnose <pod-name> [--namespace k8s-diagnose]
//   kubewhy diagnose --all    [--namespace k8s-diagnose]
//
// Dependencies (resolved by go mod tidy):
//   github.com/anthropics/anthropic-sdk-go
//   k8s.io/client-go

package main

import (
	"context"
	"fmt"
	"os"

	flag "github.com/spf13/pflag"
	"path/filepath"
	"sort"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// ── data model ───────────────────────────────────────────────────────────────

type stateSnap struct {
	Reason    string `json:"reason,omitempty"`
	Message   string `json:"message,omitempty"`
	ExitCode  int32  `json:"exit_code,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type containerSnap struct {
	Name         string    `json:"name"`
	Image        string    `json:"image"`
	Ready        bool      `json:"ready"`
	RestartCount int32     `json:"restart_count"`
	State        stateSnap `json:"state"`
	LastState    stateSnap `json:"last_state,omitempty"`
}

type conditionSnap struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type eventSnap struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Count   int32  `json:"count"`
	Age     string `json:"age"`
}

type podSnapshot struct {
	Name           string          `json:"name"`
	Namespace      string          `json:"namespace"`
	Phase          string          `json:"phase"`
	Conditions     []conditionSnap `json:"conditions"`
	InitContainers []containerSnap `json:"init_containers,omitempty"`
	Containers     []containerSnap `json:"containers"`
	Events         []eventSnap     `json:"events"`
	Logs           map[string]string `json:"logs"`
}

// ── kubernetes helpers ────────────────────────────────────────────────────────

func buildClient() (*kubernetes.Clientset, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			if home := homedir.HomeDir(); home != "" {
				kubeconfig = filepath.Join(home, ".kube", "config")
			}
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("cannot build kubeconfig: %w", err)
		}
	}
	return kubernetes.NewForConfig(cfg)
}

func isBroken(pod corev1.Pod) bool {
	phase := pod.Status.Phase
	if phase == corev1.PodFailed || phase == corev1.PodUnknown {
		return true
	}
	// Pending: check for unschedulable or bad container reason
	if phase == corev1.PodPending {
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
				return true
			}
		}
		for _, cs := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
			if cs.State.Waiting != nil {
				r := cs.State.Waiting.Reason
				if r == "ImagePullBackOff" || r == "ErrImagePull" ||
					r == "CreateContainerConfigError" || r == "InvalidImageName" {
					return true
				}
			}
		}
		return false
	}
	// Running: high restart count signals CrashLoop / liveness kills
	if phase == corev1.PodRunning {
		for _, cs := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
			if cs.RestartCount >= 3 {
				return true
			}
		}
	}
	return false
}

func snapState(s *corev1.ContainerState) stateSnap {
	if s == nil {
		return stateSnap{}
	}
	if s.Waiting != nil {
		return stateSnap{Reason: s.Waiting.Reason, Message: s.Waiting.Message}
	}
	if s.Terminated != nil {
		snap := stateSnap{
			Reason:   s.Terminated.Reason,
			Message:  s.Terminated.Message,
			ExitCode: s.Terminated.ExitCode,
		}
		if !s.Terminated.StartedAt.IsZero() {
			snap.StartedAt = s.Terminated.StartedAt.Format(time.RFC3339)
		}
		if !s.Terminated.FinishedAt.IsZero() {
			snap.FinishedAt = s.Terminated.FinishedAt.Format(time.RFC3339)
		}
		return snap
	}
	if s.Running != nil {
		return stateSnap{Reason: "Running"}
	}
	return stateSnap{}
}

func snapContainers(statuses []corev1.ContainerStatus, specs []corev1.Container) []containerSnap {
	imageByName := map[string]string{}
	for _, s := range specs {
		imageByName[s.Name] = s.Image
	}
	var out []containerSnap
	for _, cs := range statuses {
		snap := containerSnap{
			Name:         cs.Name,
			Image:        imageByName[cs.Name],
			Ready:        cs.Ready,
			RestartCount: cs.RestartCount,
			State:        snapState(&cs.State),
			LastState:    snapState(&cs.LastTerminationState),
		}
		out = append(out, snap)
	}
	return out
}

func fetchLogs(ctx context.Context, clientset *kubernetes.Clientset, ns, podName, containerName string, previous bool) string {
	tailLines := int64(40)
	req := clientset.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{
		Container: containerName,
		TailLines: &tailLines,
		Previous:  previous,
	})
	data, err := req.DoRaw(ctx)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func collectSnapshot(ctx context.Context, clientset *kubernetes.Clientset, pod corev1.Pod) podSnapshot {
	ns := pod.Namespace
	name := pod.Name

	// conditions
	var conds []conditionSnap
	for _, c := range pod.Status.Conditions {
		conds = append(conds, conditionSnap{
			Type:    string(c.Type),
			Status:  string(c.Status),
			Reason:  c.Reason,
			Message: c.Message,
		})
	}

	// events (last 15)
	evList, _ := clientset.CoreV1().Events(ns).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s", name),
	})
	type evWithTime struct {
		ev   corev1.Event
		last time.Time
	}
	var evts []evWithTime
	if evList != nil {
		for _, e := range evList.Items {
			evts = append(evts, evWithTime{ev: e, last: e.LastTimestamp.Time})
		}
	}
	sort.Slice(evts, func(i, j int) bool { return evts[i].last.Before(evts[j].last) })
	if len(evts) > 15 {
		evts = evts[len(evts)-15:]
	}
	var events []eventSnap
	for _, e := range evts {
		age := "unknown"
		if !e.last.IsZero() {
			age = time.Since(e.last).Round(time.Second).String()
		}
		events = append(events, eventSnap{
			Reason:  e.ev.Reason,
			Message: e.ev.Message,
			Count:   e.ev.Count,
			Age:     age,
		})
	}

	// logs
	logs := map[string]string{}
	allStatuses := append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...)
	for _, cs := range allStatuses {
		if l := fetchLogs(ctx, clientset, ns, name, cs.Name, false); l != "" {
			logs[cs.Name+"/current"] = l
		}
		if cs.RestartCount > 0 {
			if l := fetchLogs(ctx, clientset, ns, name, cs.Name, true); l != "" {
				logs[cs.Name+"/previous"] = l
			}
		}
	}

	return podSnapshot{
		Name:           name,
		Namespace:      ns,
		Phase:          string(pod.Status.Phase),
		Conditions:     conds,
		InitContainers: snapContainers(pod.Status.InitContainerStatuses, pod.Spec.InitContainers),
		Containers:     snapContainers(pod.Status.ContainerStatuses, pod.Spec.Containers),
		Events:         events,
		Logs:           logs,
	}
}

// ── prompt builder ────────────────────────────────────────────────────────────

func buildPrompt(snaps []podSnapshot) string {
	var sb strings.Builder
	sb.WriteString("You are diagnosing broken Kubernetes pods. For each pod below, identify the root cause and provide specific remediation steps.\n\n")

	for i, s := range snaps {
		sb.WriteString(fmt.Sprintf("## Pod %d: %s (namespace: %s)\n", i+1, s.Name, s.Namespace))
		sb.WriteString(fmt.Sprintf("Phase: %s\n\n", s.Phase))

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

		printContainers := func(label string, containers []containerSnap) {
			if len(containers) == 0 {
				return
			}
			sb.WriteString(fmt.Sprintf("### %s\n", label))
			for _, c := range containers {
				sb.WriteString(fmt.Sprintf("- **%s** (image: %s, restarts: %d, ready: %v)\n",
					c.Name, c.Image, c.RestartCount, c.Ready))
				if c.State.Reason != "" {
					sb.WriteString(fmt.Sprintf("  State: %s", c.State.Reason))
					if c.State.Message != "" {
						sb.WriteString(fmt.Sprintf(" — %s", c.State.Message))
					}
					if c.State.ExitCode != 0 {
						sb.WriteString(fmt.Sprintf(" (exit %d)", c.State.ExitCode))
					}
					sb.WriteString("\n")
				}
				if c.LastState.Reason != "" {
					sb.WriteString(fmt.Sprintf("  Last: %s (exit %d)\n", c.LastState.Reason, c.LastState.ExitCode))
				}
			}
			sb.WriteString("\n")
		}
		printContainers("Init Containers", s.InitContainers)
		printContainers("Containers", s.Containers)

		if len(s.Events) > 0 {
			sb.WriteString("### Recent Events\n")
			for _, e := range s.Events {
				sb.WriteString(fmt.Sprintf("- [%s] %s (×%d, %s ago)\n", e.Reason, e.Message, e.Count, e.Age))
			}
			sb.WriteString("\n")
		}

		if len(s.Logs) > 0 {
			sb.WriteString("### Logs\n")
			for key, text := range s.Logs {
				sb.WriteString(fmt.Sprintf("**%s:**\n```\n%s\n```\n\n", key, text))
			}
		}

		sb.WriteString("---\n\n")
	}

	sb.WriteString("For each pod: state the diagnosis (1-2 sentences), then list concrete fix steps.\n")
	return sb.String()
}

// ── main ──────────────────────────────────────────────────────────────────────

func usage() {
	fmt.Fprintf(os.Stderr, `kubewhy — AI-powered Kubernetes pod diagnosis

Usage:
  kubewhy diagnose <pod-name> [flags]   diagnose a specific pod
  kubewhy diagnose --all     [flags]   diagnose all broken pods in namespace

Flags:
  -n, --namespace string   Kubernetes namespace (default "default")
      --all                Scan and diagnose all broken pods

Examples:
  kubewhy diagnose crash-loop-abc123 --namespace k8s-diagnose
  kubewhy diagnose --all --namespace k8s-diagnose
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
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
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

	clientset, err := buildClient()
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
			if isBroken(p) {
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
	var snaps []podSnapshot
	for _, p := range targets {
		fmt.Printf("  • %s (%s)\n", p.Name, p.Status.Phase)
		snaps = append(snaps, collectSnapshot(ctx, clientset, p))
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
