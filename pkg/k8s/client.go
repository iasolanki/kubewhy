// Package k8s provides shared Kubernetes client and pod utilities.
package k8s

import (
	"fmt"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// BuildClient returns a Kubernetes clientset using in-cluster config if
// available, falling back to KUBECONFIG or ~/.kube/config.
func BuildClient() (*kubernetes.Clientset, error) {
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

// IsBroken reports whether a pod is in a failure state worth diagnosing.
func IsBroken(pod corev1.Pod) bool {
	phase := pod.Status.Phase
	if phase == corev1.PodFailed || phase == corev1.PodUnknown {
		return true
	}
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
	if phase == corev1.PodRunning {
		for _, cs := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
			if cs.RestartCount >= 3 {
				return true
			}
		}
	}
	return false
}
