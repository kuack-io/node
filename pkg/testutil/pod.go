package testutil

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// PodOption customizes a test pod fixture.
type PodOption func(*corev1.Pod)

// NewPod returns a baseline pod that callers can customize with PodOption helpers.
func NewPod(name, namespace string, opts ...PodOption) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(name + "-uid"),
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "container-1",
					Image: "nginx",
				},
			},
		},
	}

	for _, opt := range opts {
		opt(pod)
	}

	return pod
}

// WithContainerResources configures CPU/memory requests on the first container.
func WithContainerResources(cpu, memory string) PodOption {
	return func(pod *corev1.Pod) {
		if len(pod.Spec.Containers) == 0 {
			pod.Spec.Containers = []corev1.Container{{Name: "container-1", Image: "nginx"}}
		}

		resources := &pod.Spec.Containers[0].Resources
		if cpu != "" {
			resources.Requests = ensureResourceMap(resources.Requests)
			resources.Requests[corev1.ResourceCPU] = resource.MustParse(cpu)
		}

		if memory != "" {
			resources.Requests = ensureResourceMap(resources.Requests)
			resources.Requests[corev1.ResourceMemory] = resource.MustParse(memory)
		}
	}
}

// WithPhase sets the pod status phase.
func WithPhase(phase corev1.PodPhase) PodOption {
	return func(pod *corev1.Pod) {
		pod.Status.Phase = phase
	}
}

func ensureResourceMap(m corev1.ResourceList) corev1.ResourceList {
	if m == nil {
		return make(corev1.ResourceList)
	}

	return m
}
