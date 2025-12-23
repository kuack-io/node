package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/virtual-kubelet/virtual-kubelet/errdefs"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const (
	// KubeletPort is the standard port for the kubelet endpoint.
	KubeletPort = 10250
	// logVerboseLevel is the klog verbosity level for verbose debug logs.
	logVerboseLevel = 4
	// TaintKeyKuackProvider is the taint key for kuack.io/provider.
	TaintKeyKuackProvider = "kuack.io/provider"
	// TaintValueKuack is the taint value for kuack provider.
	TaintValueKuack = "kuack"
)

var (
	// ErrNoSuitableAgent is returned when no suitable agent is found for pod scheduling.
	ErrNoSuitableAgent = errors.New("no suitable agent found")
	// ErrInvalidAgentType is returned when an agent has an invalid type.
	ErrInvalidAgentType = errors.New("invalid agent type")
	// ErrPodHasNoContainers is returned when a pod has no containers.
	ErrPodHasNoContainers = errors.New("pod has no containers")
	// ErrAgentStreamNotWebSocket is returned when agent stream is not a WebSocket connection.
	ErrAgentStreamNotWebSocket = errors.New("agent stream is not a WebSocket connection")
)

const (
	// UnschedulableReason is the reason for unschedulable pods.
	UnschedulableReason = "Unschedulable"
)

// WASMProvider implements the virtual-kubelet provider interface for browser-based WASM agents.
type WASMProvider struct {
	nodeName   string
	agents     sync.Map // UUID -> *AgentConnection
	pods       sync.Map // namespace/name -> *corev1.Pod
	resources  *ResourceTracker
	notifyFunc func(*corev1.Pod)
	startTime  time.Time
	mu         sync.RWMutex
}

// AgentConnection represents a connected browser agent.
type AgentConnection struct {
	UUID          string
	BrowserID     string // Persistent browser identifier (from localStorage)
	Stream        any    // WebTransport stream
	Resources     ResourceSpec
	AllocatedPods map[string]*corev1.Pod
	LastHeartbeat time.Time
	IsThrottled   bool
	Labels        map[string]string
	mu            sync.RWMutex
}

// ResourceSpec describes agent capabilities.
type ResourceSpec struct {
	CPU    resource.Quantity
	Memory resource.Quantity
	GPU    bool
}

// ResourceTracker aggregates resources across all agents.
type ResourceTracker struct {
	totalCapacity  corev1.ResourceList
	totalAllocated corev1.ResourceList
	perAgentCap    sync.Map // UUID -> ResourceSpec
	mu             sync.RWMutex
}

// NewWASMProvider creates a new WASM provider instance.
func NewWASMProvider(nodeName string) (*WASMProvider, error) {
	klog.Infof("Initializing WASM provider for node: %s", nodeName)

	return &WASMProvider{
		nodeName:  nodeName,
		resources: NewResourceTracker(),
		startTime: time.Now(),
	}, nil
}

// CreatePod accepts a new pod and schedules it to an available agent.
// Returns an error if no agent with sufficient capacity is available.
func (p *WASMProvider) CreatePod(ctx context.Context, pod *corev1.Pod) error {
	klog.Infof("CreatePod called for %s/%s", pod.Namespace, pod.Name)

	// Deep copy to ensure thread safety
	pod = pod.DeepCopy()

	// Select an appropriate agent with sufficient capacity
	agentUUID, err := p.selectAgent(pod)
	if err != nil {
		// No agent with sufficient capacity available - reject the pod
		klog.Warningf("No suitable agent found for pod %s/%s: %v", pod.Namespace, pod.Name, err)

		return errdefs.AsInvalidInput(err)
	}

	// Load the agent
	agentVal, ok := p.agents.Load(agentUUID)
	if !ok {
		// Agent disappeared between selection and loading
		klog.Warningf("Agent %s not found after selection for pod %s/%s", agentUUID, pod.Namespace, pod.Name)

		return errdefs.NotFound("agent not found")
	}

	agent, ok := agentVal.(*AgentConnection)
	if !ok {
		return errdefs.AsInvalidInput(ErrInvalidAgentType)
	}

	// Double-check capacity before allocating (race condition protection)
	agent.mu.RLock()
	hasCapacity := agent.HasCapacity(pod)
	agent.mu.RUnlock()

	if !hasCapacity {
		klog.Warningf("Agent %s no longer has sufficient capacity for pod %s/%s", agentUUID, pod.Namespace, pod.Name)

		return errdefs.AsInvalidInput(ErrNoSuitableAgent)
	}

	// Assign pod to agent
	agent.mu.Lock()
	agent.AllocatedPods[getPodKey(pod)] = pod
	agent.mu.Unlock()

	// Store pod
	p.pods.Store(getPodKey(pod), pod)

	// Send pod specification to agent via WebSocket
	err = p.sendPodToAgent(agent, pod)
	if err != nil {
		// Remove from agent allocation on send failure
		agent.mu.Lock()
		delete(agent.AllocatedPods, getPodKey(pod))
		agent.mu.Unlock()

		// Remove from stored pods
		p.pods.Delete(getPodKey(pod))

		klog.Errorf("Failed to send pod to agent: %v", err)

		return err
	}

	// Update pod status to Pending (scheduled)
	pod.Status.Phase = corev1.PodPending
	pod.Status.Conditions = []corev1.PodCondition{
		{
			Type:               corev1.PodScheduled,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
		},
	}

	// Update stored pod with status
	p.pods.Store(getPodKey(pod), pod)

	// Update resource allocation
	p.resources.AllocatePod(pod)

	// Notify Kubernetes about the scheduled status
	if p.notifyFunc != nil {
		p.notifyFunc(pod.DeepCopy())
	}

	klog.Infof("Pod %s/%s assigned to agent %s", pod.Namespace, pod.Name, agentUUID)

	return nil
}

// UpdatePod updates an existing pod.
func (p *WASMProvider) UpdatePod(ctx context.Context, pod *corev1.Pod) error {
	klog.Infof("UpdatePod called for %s/%s", pod.Namespace, pod.Name)

	pod = pod.DeepCopy()
	p.pods.Store(getPodKey(pod), pod)

	return nil
}

// DeletePod removes a pod from the provider.
func (p *WASMProvider) DeletePod(ctx context.Context, pod *corev1.Pod) error {
	klog.Infof("DeletePod called for %s/%s", pod.Namespace, pod.Name)

	podKey := getPodKey(pod)

	// Find the agent running this pod
	var targetAgent *AgentConnection

	p.agents.Range(func(key, value any) bool {
		agent, ok := value.(*AgentConnection)
		if !ok {
			return true
		}

		agent.mu.RLock()

		if _, exists := agent.AllocatedPods[podKey]; exists {
			targetAgent = agent
			agent.mu.RUnlock()

			return false
		}

		agent.mu.RUnlock()

		return true
	})

	if targetAgent != nil {
		// Send delete command to agent
		err := p.deletePodFromAgent(targetAgent, pod)
		if err != nil {
			klog.Errorf("Failed to delete pod from agent: %v", err)
		}

		// Remove from agent's allocated pods
		targetAgent.mu.Lock()
		delete(targetAgent.AllocatedPods, podKey)
		targetAgent.mu.Unlock()
	}

	// Remove from provider state
	p.pods.Delete(podKey)

	// Update resource allocation
	p.resources.DeallocatePod(pod)

	// Notify with terminal status
	pod = pod.DeepCopy()
	pod.Status.Phase = corev1.PodSucceeded

	pod.Status.Reason = "Deleted"
	if p.notifyFunc != nil {
		p.notifyFunc(pod)
	}

	return nil
}

// GetPod retrieves a specific pod.
func (p *WASMProvider) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	podKey := namespace + "/" + name

	podVal, ok := p.pods.Load(podKey)
	if !ok {
		return nil, errdefs.NotFound("pod not found")
	}

	// MUST return deep copy for thread safety
	pod, ok := podVal.(*corev1.Pod)
	if !ok {
		return nil, errdefs.NotFound("invalid pod type")
	}

	return pod.DeepCopy(), nil
}

// GetPodStatus retrieves the status of a pod.
func (p *WASMProvider) GetPodStatus(
	ctx context.Context,
	namespace, name string,
) (*corev1.PodStatus, error) {
	pod, err := p.GetPod(ctx, namespace, name)
	if err != nil {
		return nil, err
	}

	// Return deep copy of status
	status := pod.Status.DeepCopy()

	return status, nil
}

// GetPods retrieves all pods.
func (p *WASMProvider) GetPods(ctx context.Context) ([]*corev1.Pod, error) {
	var pods []*corev1.Pod

	p.pods.Range(func(key, value any) bool {
		pod, ok := value.(*corev1.Pod)
		if !ok {
			return true
		}

		// MUST return deep copy
		pods = append(pods, pod.DeepCopy())

		return true
	})

	return pods, nil
}

// NotifyPods implements PodNotifier for asynchronous status updates.
func (p *WASMProvider) NotifyPods(ctx context.Context, notifyFunc func(*corev1.Pod)) {
	klog.Info("NotifyPods callback registered")
	p.mu.Lock()
	p.notifyFunc = notifyFunc
	p.mu.Unlock()
}

// GetContainerLogs returns logs from a container.
func (p *WASMProvider) GetContainerLogs(
	ctx context.Context,
	namespace, podName, containerName string,
	opts api.ContainerLogOpts,
) (io.ReadCloser, error) {
	// TODO: Implement log streaming from agent
	return nil, errdefs.NotFound("logs not implemented yet")
}

// RunInContainer executes a command in a container.
func (p *WASMProvider) RunInContainer(
	ctx context.Context,
	namespace, podName, containerName string,
	cmd []string,
	attach api.AttachIO,
) error {
	return errdefs.NotFound("exec not implemented yet")
}

// AttachToContainer attaches to a running container.
func (p *WASMProvider) AttachToContainer(
	ctx context.Context,
	namespace, podName, containerName string,
	attach api.AttachIO,
) error {
	return errdefs.NotFound("attach not implemented yet")
}

// Ping checks if the node is still active.
// This is intended to be lightweight as it will be called periodically as a
// heartbeat to keep the node marked as ready in Kubernetes.
func (p *WASMProvider) Ping(ctx context.Context) error {
	// For WASM provider, we're always ready if we have at least one agent
	// or if we're configured to accept pods
	return nil
}

// NotifyNodeStatus is used to asynchronously monitor the node.
// The passed in callback should be called any time there is a change to the
// node's status.
// This will generally trigger a call to the Kubernetes API server to update
// the status.
//
// NotifyNodeStatus should not block callers.
func (p *WASMProvider) NotifyNodeStatus(ctx context.Context, callback func(*corev1.Node)) {
	// Store the callback for future use when node status changes
	// For now, we can call it immediately with the current node status
	// In a more sophisticated implementation, we would call this whenever
	// resources change, agents connect/disconnect, etc.
	go func() {
		node := p.GetNode()
		callback(node)
	}()
}

// GetNode returns the node configuration.
func (p *WASMProvider) GetNode() *corev1.Node {
	p.resources.mu.RLock()
	defer p.resources.mu.RUnlock()

	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: p.nodeName,
			Labels: map[string]string{
				"kuack.io/node-type":     "kuack-node",
				"kubernetes.io/role":     "agent",
				"kubernetes.io/hostname": p.nodeName,
				"alpha.service-controller.kubernetes.io/exclude-balancer": "true",
			},
		},
		Spec: corev1.NodeSpec{
			Taints: p.getTaints(),
		},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				OperatingSystem: "wasm",
				Architecture:    "wasm32",
				KubeletVersion:  "v1.29.0-kuack-node",
			},
			Capacity:    p.resources.totalCapacity,
			Allocatable: p.resources.GetAllocatable(),
			Conditions:  p.getNodeConditions(),
			Addresses: []corev1.NodeAddress{
				{
					Type:    corev1.NodeInternalIP,
					Address: "127.0.0.1",
				},
			},
			DaemonEndpoints: corev1.NodeDaemonEndpoints{
				KubeletEndpoint: corev1.DaemonEndpoint{
					Port: KubeletPort,
				},
			},
		},
	}
}

// Helper functions

func getPodKey(pod *corev1.Pod) string {
	return pod.Namespace + "/" + pod.Name
}

func (p *WASMProvider) getTaints() []corev1.Taint {
	// Always return exactly one taint: kuack.io/provider=kuack:NoSchedule
	return []corev1.Taint{
		{
			Key:    TaintKeyKuackProvider,
			Value:  TaintValueKuack,
			Effect: corev1.TaintEffectNoSchedule,
		},
	}
}

func (p *WASMProvider) selectAgent(pod *corev1.Pod) (string, error) {
	var (
		selectedAgent string
		maxAvailable  resource.Quantity
	)

	// Initialize maxAvailable to zero (empty quantity)
	maxAvailable = resource.MustParse("0")

	// Simple first-fit strategy for MVP. TODO: Implement more sophisticated scheduling.

	p.agents.Range(func(key, value any) bool {
		uuid, ok := key.(string)
		if !ok {
			return true
		}

		agent, ok := value.(*AgentConnection)
		if !ok {
			return true
		}

		// Skip throttled agents
		if agent.IsThrottled {
			return true
		}

		// Check if agent has enough resources
		if agent.HasCapacity(pod) {
			// Calculate available CPU for this agent
			agent.mu.RLock()

			var allocatedCPU resource.Quantity

			for _, allocatedPod := range agent.AllocatedPods {
				for _, container := range allocatedPod.Spec.Containers {
					if cpu, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
						allocatedCPU.Add(cpu)
					}
				}
			}

			availableCPU := agent.Resources.CPU.DeepCopy()
			availableCPU.Sub(allocatedCPU)
			agent.mu.RUnlock()

			// Select agent with most available CPU
			if availableCPU.Cmp(maxAvailable) > 0 {
				maxAvailable = availableCPU
				selectedAgent = uuid
			}
		}

		return true
	})

	if selectedAgent == "" {
		return "", ErrNoSuitableAgent
	}

	return selectedAgent, nil
}

func (a *AgentConnection) HasCapacity(pod *corev1.Pod) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// Calculate total requested resources for the pod
	var requestedCPU, requestedMemory resource.Quantity

	for _, container := range pod.Spec.Containers {
		if cpu, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
			requestedCPU.Add(cpu)
		}

		if mem, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
			requestedMemory.Add(mem)
		}
	}

	// Calculate currently allocated resources
	var allocatedCPU, allocatedMemory resource.Quantity

	for _, allocatedPod := range a.AllocatedPods {
		for _, container := range allocatedPod.Spec.Containers {
			if cpu, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
				allocatedCPU.Add(cpu)
			}

			if mem, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
				allocatedMemory.Add(mem)
			}
		}
	}

	// Calculate available resources
	availableCPU := a.Resources.CPU.DeepCopy()
	availableCPU.Sub(allocatedCPU)

	availableMemory := a.Resources.Memory.DeepCopy()
	availableMemory.Sub(allocatedMemory)

	// Check if available resources are sufficient
	hasCPU := availableCPU.Cmp(requestedCPU) >= 0
	hasMemory := availableMemory.Cmp(requestedMemory) >= 0

	return hasCPU && hasMemory
}

// AgentPodSpec represents the pod spec format expected by the agent.
type AgentPodSpec struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Containers []AgentContainerSpec `json:"containers"`
	} `json:"spec"`
}

// AgentContainerSpec represents a container spec in the agent format.
type AgentContainerSpec struct {
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	Command []string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Env     []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"env,omitempty"`
	Wasm *struct {
		Path    string `json:"path,omitempty"`
		Variant string `json:"variant,omitempty"`
		Image   string `json:"image,omitempty"`
	} `json:"wasm,omitempty"`
}

// convertPodToAgentSpec converts a Kubernetes Pod to the agent's PodSpec format.
func convertPodToAgentSpec(pod *corev1.Pod) (*AgentPodSpec, error) {
	if len(pod.Spec.Containers) == 0 {
		return nil, ErrPodHasNoContainers
	}

	agentSpec := &AgentPodSpec{}
	agentSpec.Metadata.Name = pod.Name
	agentSpec.Metadata.Namespace = pod.Namespace

	agentSpec.Spec.Containers = make([]AgentContainerSpec, 0, len(pod.Spec.Containers))

	for _, container := range pod.Spec.Containers {
		agentContainer := AgentContainerSpec{
			Name:    container.Name,
			Image:   container.Image,
			Command: container.Command,
			Args:    container.Args,
		}

		// Convert environment variables
		if len(container.Env) > 0 {
			agentContainer.Env = make([]struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			}, 0, len(container.Env))

			for _, env := range container.Env {
				envEntry := struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				}{
					Name:  env.Name,
					Value: env.Value,
				}
				agentContainer.Env = append(agentContainer.Env, envEntry)
			}
		}

		// Check for WASM-specific annotations
		// Look for annotations that might indicate WASM configuration
		if pod.Annotations != nil {
			wasmPath := pod.Annotations["kuack.io/wasm-path"]
			wasmVariant := pod.Annotations["kuack.io/wasm-variant"]
			wasmImage := pod.Annotations["kuack.io/wasm-image"]

			if wasmPath != "" || wasmVariant != "" || wasmImage != "" {
				agentContainer.Wasm = &struct {
					Path    string `json:"path,omitempty"`
					Variant string `json:"variant,omitempty"`
					Image   string `json:"image,omitempty"`
				}{
					Path:    wasmPath,
					Variant: wasmVariant,
					Image:   wasmImage,
				}
			}
		}

		agentSpec.Spec.Containers = append(agentSpec.Spec.Containers, agentContainer)
	}

	return agentSpec, nil
}

func (p *WASMProvider) sendPodToAgent(agent *AgentConnection, pod *corev1.Pod) error {
	klog.V(logVerboseLevel).Infof("Sending pod %s to agent %s", getPodKey(pod), agent.UUID)

	// Convert pod to agent format
	agentSpec, err := convertPodToAgentSpec(pod)
	if err != nil {
		return err
	}

	// Serialize to JSON
	data, err := json.Marshal(agentSpec)
	if err != nil {
		return err
	}

	// Get WebSocket connection
	conn, ok := agent.Stream.(*websocket.Conn)
	if !ok {
		return ErrAgentStreamNotWebSocket
	}

	// Create message in the format expected by the agent
	// Using time.Time will be marshaled as RFC3339 (ISO 8601), matching the agent's expectation
	msg := struct {
		Type      string          `json:"type"`
		Timestamp time.Time       `json:"timestamp"`
		Data      json.RawMessage `json:"data"`
	}{
		Type:      "pod_spec",
		Timestamp: time.Now(),
		Data:      json.RawMessage(data),
	}

	// Send message via WebSocket
	err = conn.WriteJSON(msg)
	if err != nil {
		return err
	}

	klog.V(logVerboseLevel).Infof("Successfully sent pod spec to agent %s", agent.UUID)

	return nil
}

//nolint:unparam // TODO: Implement actual deletion logic that may return errors
func (p *WASMProvider) deletePodFromAgent(agent *AgentConnection, pod *corev1.Pod) error {
	// TODO: Send delete command via WebSocket
	klog.V(logVerboseLevel).Infof("Deleting pod %s from agent %s", getPodKey(pod), agent.UUID)

	return nil
}

func (p *WASMProvider) getNodeConditions() []corev1.NodeCondition {
	now := metav1.Now()

	return []corev1.NodeCondition{
		{
			Type:               corev1.NodeReady,
			Status:             corev1.ConditionTrue,
			LastHeartbeatTime:  now,
			LastTransitionTime: metav1.NewTime(p.startTime),
			Reason:             "KubeletReady",
			Message:            "WASM provider is ready",
		},
		{
			Type:               corev1.NodeMemoryPressure,
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  now,
			LastTransitionTime: metav1.NewTime(p.startTime),
			Reason:             "KubeletHasSufficientMemory",
		},
		{
			Type:               corev1.NodeDiskPressure,
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  now,
			LastTransitionTime: metav1.NewTime(p.startTime),
			Reason:             "KubeletHasNoDiskPressure",
		},
		{
			Type:               corev1.NodePIDPressure,
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  now,
			LastTransitionTime: metav1.NewTime(p.startTime),
			Reason:             "KubeletHasSufficientPID",
		},
		{
			Type:               corev1.NodeNetworkUnavailable,
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  now,
			LastTransitionTime: metav1.NewTime(p.startTime),
			Reason:             "RouteCreated",
		},
	}
}

// NewResourceTracker creates a new resource tracker.
func NewResourceTracker() *ResourceTracker {
	return &ResourceTracker{
		totalCapacity: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("0"),
			corev1.ResourceMemory: resource.MustParse("0"),
			corev1.ResourcePods:   resource.MustParse("110"),
		},
		totalAllocated: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("0"),
			corev1.ResourceMemory: resource.MustParse("0"),
		},
	}
}

func (rt *ResourceTracker) GetAllocatable() corev1.ResourceList {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	cpuAvail := rt.totalCapacity[corev1.ResourceCPU].DeepCopy()
	cpuAvail.Sub(rt.totalAllocated[corev1.ResourceCPU])

	memAvail := rt.totalCapacity[corev1.ResourceMemory].DeepCopy()
	memAvail.Sub(rt.totalAllocated[corev1.ResourceMemory])

	return corev1.ResourceList{
		corev1.ResourceCPU:    cpuAvail,
		corev1.ResourceMemory: memAvail,
		corev1.ResourcePods:   rt.totalCapacity[corev1.ResourcePods],
	}
}

func (rt *ResourceTracker) AllocatePod(pod *corev1.Pod) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	for _, container := range pod.Spec.Containers {
		if cpu, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
			current := rt.totalAllocated[corev1.ResourceCPU]
			current.Add(cpu)
			rt.totalAllocated[corev1.ResourceCPU] = current
		}

		if mem, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
			current := rt.totalAllocated[corev1.ResourceMemory]
			current.Add(mem)
			rt.totalAllocated[corev1.ResourceMemory] = current
		}
	}
}

func (rt *ResourceTracker) DeallocatePod(pod *corev1.Pod) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	for _, container := range pod.Spec.Containers {
		if cpu, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
			current := rt.totalAllocated[corev1.ResourceCPU]
			current.Sub(cpu)
			rt.totalAllocated[corev1.ResourceCPU] = current
		}

		if mem, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
			current := rt.totalAllocated[corev1.ResourceMemory]
			current.Sub(mem)
			rt.totalAllocated[corev1.ResourceMemory] = current
		}
	}
}

func (rt *ResourceTracker) AddAgent(uuid string, resources ResourceSpec) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	rt.perAgentCap.Store(uuid, resources)
	cpu := rt.totalCapacity[corev1.ResourceCPU]
	cpu.Add(resources.CPU)
	rt.totalCapacity[corev1.ResourceCPU] = cpu

	mem := rt.totalCapacity[corev1.ResourceMemory]
	mem.Add(resources.Memory)
	rt.totalCapacity[corev1.ResourceMemory] = mem
}

func (rt *ResourceTracker) RemoveAgent(uuid string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if val, ok := rt.perAgentCap.LoadAndDelete(uuid); ok {
		resources, ok := val.(ResourceSpec)
		if !ok {
			return
		}

		cpu := rt.totalCapacity[corev1.ResourceCPU]
		cpu.Sub(resources.CPU)
		rt.totalCapacity[corev1.ResourceCPU] = cpu

		mem := rt.totalCapacity[corev1.ResourceMemory]
		mem.Sub(resources.Memory)
		rt.totalCapacity[corev1.ResourceMemory] = mem
	}
}
