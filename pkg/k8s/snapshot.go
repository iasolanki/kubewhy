package k8s

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ── types ─────────────────────────────────────────────────────────────────────

type StateSnap struct {
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message,omitempty"`
	ExitCode   int32  `json:"exit_code,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type ContainerSnap struct {
	Name         string    `json:"name"`
	Image        string    `json:"image"`
	Ready        bool      `json:"ready"`
	RestartCount int32     `json:"restart_count"`
	State        StateSnap `json:"state"`
	LastState    StateSnap `json:"last_state,omitempty"`
}

type ConditionSnap struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type EventSnap struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Count   int32  `json:"count"`
	Age     string `json:"age"`
}

type PodSnapshot struct {
	Name           string            `json:"name"`
	Namespace      string            `json:"namespace"`
	Phase          string            `json:"phase"`
	Conditions     []ConditionSnap   `json:"conditions"`
	InitContainers []ContainerSnap   `json:"init_containers,omitempty"`
	Containers     []ContainerSnap   `json:"containers"`
	Events         []EventSnap       `json:"events"`
	Logs           map[string]string `json:"logs"`
}

// ── collection ────────────────────────────────────────────────────────────────

func CollectSnapshot(ctx context.Context, clientset *kubernetes.Clientset, pod corev1.Pod) PodSnapshot {
	ns := pod.Namespace
	name := pod.Name

	var conds []ConditionSnap
	for _, c := range pod.Status.Conditions {
		conds = append(conds, ConditionSnap{
			Type:    string(c.Type),
			Status:  string(c.Status),
			Reason:  c.Reason,
			Message: c.Message,
		})
	}

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
	var events []EventSnap
	for _, e := range evts {
		age := "unknown"
		if !e.last.IsZero() {
			age = time.Since(e.last).Round(time.Second).String()
		}
		events = append(events, EventSnap{
			Reason:  e.ev.Reason,
			Message: e.ev.Message,
			Count:   e.ev.Count,
			Age:     age,
		})
	}

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

	return PodSnapshot{
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

// ── helpers ───────────────────────────────────────────────────────────────────

func snapState(s *corev1.ContainerState) StateSnap {
	if s == nil {
		return StateSnap{}
	}
	if s.Waiting != nil {
		return StateSnap{Reason: s.Waiting.Reason, Message: s.Waiting.Message}
	}
	if s.Terminated != nil {
		snap := StateSnap{
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
		return StateSnap{Reason: "Running"}
	}
	return StateSnap{}
}

func snapContainers(statuses []corev1.ContainerStatus, specs []corev1.Container) []ContainerSnap {
	imageByName := map[string]string{}
	for _, s := range specs {
		imageByName[s.Name] = s.Image
	}
	var out []ContainerSnap
	for _, cs := range statuses {
		out = append(out, ContainerSnap{
			Name:         cs.Name,
			Image:        imageByName[cs.Name],
			Ready:        cs.Ready,
			RestartCount: cs.RestartCount,
			State:        snapState(&cs.State),
			LastState:    snapState(&cs.LastTerminationState),
		})
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
