package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

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
	// writeTimeout is the timeout for writing to WebSocket connections.
	writeTimeout = 10 * time.Second
	// logListenerBufferSize is the buffered channel size for log listeners.
	logListenerBufferSize = 100
)

var (
	// ErrNoSuitableAgent is returned when no suitable agent is found for pod scheduling.
	ErrNoSuitableAgent = errors.New("no suitable agent found")
	// ErrPodHasNoContainers is returned when a pod has no containers.
	ErrPodHasNoContainers = errors.New("pod has no containers")
	// ErrAgentStreamNotWebSocket is returned when agent stream is not a WebSocket connection.
	ErrAgentStreamNotWebSocket = errors.New("agent stream is not a WebSocket connection")
	errUnknownAgentPodPhase    = errors.New("unknown agent pod phase")
	errInvalidLogStream        = errors.New("unexpected log stream type")
)

const (
	// UnschedulableReason is the reason for unschedulable pods.
	UnschedulableReason = "Unschedulable"
)

// WASMProvider implements the virtual-kubelet provider interface for browser-based WASM agents.
type WASMProvider struct {
	nodeName         string
	nodeIP           string // Pod IP or service DNS name
	agentRegistry    *AgentRegistry
	pods             sync.Map // namespace/name -> *corev1.Pod
	podLogs          sync.Map // namespace/name -> *PodLogStream
	resources        *ResourceTracker
	notifyFunc       func(*corev1.Pod)
	nodeStatusNotify func(*corev1.Node)
	startTime        time.Time
	mu               sync.RWMutex
}

// ResourceTracker aggregates resources across all agents.
type ResourceTracker struct {
	totalCapacity  corev1.ResourceList
	totalAllocated corev1.ResourceList
	perAgentCap    sync.Map // UUID -> ResourceSpec
	mu             sync.RWMutex
}

// PodLogStream manages log streaming for a pod.
type PodLogStream struct {
	mu        sync.RWMutex
	buffer    []byte
	listeners map[*LogListener]struct{}
}

// LogListener receives log updates.
type LogListener struct {
	ch   chan []byte
	done chan struct{}
}

// LogReader implements io.ReadCloser for log streaming.
type LogReader struct {
	stream   *PodLogStream
	listener *LogListener
	buffer   []byte
}

func (r *LogReader) Read(p []byte) (int, error) {
	if len(r.buffer) > 0 {
		n := copy(p, r.buffer)
		r.buffer = r.buffer[n:]

		return n, nil
	}

	if r.listener == nil {
		return 0, io.EOF
	}

	select {
	case data, ok := <-r.listener.ch:
		if !ok {
			return 0, io.EOF
		}

		n := copy(p, data)
		if n < len(data) {
			r.buffer = data[n:]
		}

		return n, nil
	case <-r.listener.done:
		return 0, io.EOF
	}
}

func (r *LogReader) Close() error {
	if r.listener != nil {
		r.stream.RemoveListener(r.listener)
	}

	return nil
}

func NewPodLogStream() *PodLogStream {
	return &PodLogStream{
		listeners: make(map[*LogListener]struct{}),
	}
}

func (s *PodLogStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.buffer = append(s.buffer, p...)

	for l := range s.listeners {
		select {
		case l.ch <- p:
		case <-l.done:
		default:
			// Drop if blocked to prevent stalling
		}
	}

	return len(p), nil
}

func (s *PodLogStream) Subscribe(follow bool) io.ReadCloser {
	s.mu.Lock()
	defer s.mu.Unlock()

	currentLogs := make([]byte, len(s.buffer))
	copy(currentLogs, s.buffer)

	if !follow {
		return &LogReader{
			buffer: currentLogs,
		}
	}

	l := &LogListener{
		ch:   make(chan []byte, logListenerBufferSize),
		done: make(chan struct{}),
	}
	s.listeners[l] = struct{}{}

	return &LogReader{
		stream:   s,
		listener: l,
		buffer:   currentLogs,
	}
}

func (s *PodLogStream) RemoveListener(l *LogListener) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.listeners, l)
	close(l.done)
	close(l.ch)
}

// NewWASMProvider creates a new WASM provider instance.
func NewWASMProvider(nodeName string) (*WASMProvider, error) {
	klog.Infof("Initializing WASM provider for node: %s", nodeName)

	// Detect node IP from environment (POD_IP) or use service name as fallback
	nodeIP := os.Getenv("POD_IP")
	if nodeIP == "" {
		// In Kubernetes, use the service name which will be resolved by DNS
		nodeIP = "kuack-node.kuack.svc.cluster.local"
		klog.Infof("POD_IP not set, using service name: %s", nodeIP)
	} else {
		klog.Infof("Using POD_IP: %s", nodeIP)
	}

	return &WASMProvider{
		nodeName:      nodeName,
		nodeIP:        nodeIP,
		agentRegistry: NewAgentRegistry(),
		resources:     NewResourceTracker(),
		startTime:     time.Now(),
	}, nil
}

// CreatePod accepts a new pod and schedules it to an available agent.
// Returns an error if no agent with sufficient capacity is available.
func (p *WASMProvider) CreatePod(ctx context.Context, pod *corev1.Pod) error {
	klog.Infof("CreatePod called for %s/%s", pod.Namespace, pod.Name)

	// Deep copy to ensure thread safety
	pod = pod.DeepCopy()

	// Store pod immediately
	p.pods.Store(getPodKey(pod), pod)

	// Set initial status to Pending
	pod.Status.Phase = corev1.PodPending
	pod.Status.Reason = "Pending"
	pod.Status.Message = "Waiting for agent with sufficient capacity"
	pod.Status.Conditions = []corev1.PodCondition{
		{
			Type:               corev1.PodScheduled,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "Scheduled",
			Message:            "Pod has been assigned to virtual node",
		},
	}

	// Notify Kubernetes about the initial pending status
	if p.notifyFunc != nil {
		p.notifyFunc(pod.DeepCopy())
	}

	// Select an appropriate agent with sufficient capacity
	agentUUID, err := p.selectAgent(pod)
	if err != nil {
		// No agent with sufficient capacity available
		// Keep pod in Pending state - it will be retried when agents connect
		klog.Infof("No suitable agent found for pod %s/%s yet: %v. Pod will remain pending.", pod.Namespace, pod.Name, err)

		return nil
	}

	// Load the agent
	agent, ok := p.agentRegistry.Get(agentUUID)
	if !ok {
		// Agent disappeared between selection and loading
		klog.Warningf("Agent %s not found after selection for pod %s/%s", agentUUID, pod.Namespace, pod.Name)

		return errdefs.NotFound("agent not found")
	}

	// Double-check capacity before allocating (race condition protection)
	agent.mu.RLock()
	hasCapacity := agent.HasCapacity(pod)
	agent.mu.RUnlock()

	if !hasCapacity {
		// Agent no longer has capacity - keep pod pending for retry
		klog.Infof("Agent %s no longer has sufficient capacity for pod %s/%s. Pod will remain pending.", agentUUID, pod.Namespace, pod.Name)

		return nil
	}

	// Assign pod to agent
	agent.mu.Lock()
	agent.AllocatedPods[getPodKey(pod)] = pod
	agent.mu.Unlock()

	// Send pod specification to agent via WebSocket
	err = p.sendPodToAgent(agent, pod)
	if err != nil {
		// Remove from agent allocation on send failure
		agent.mu.Lock()
		delete(agent.AllocatedPods, getPodKey(pod))
		agent.mu.Unlock()

		klog.Errorf("Failed to send pod to agent: %v", err)

		return err
	}

	// Update pod status to Running (being executed by agent)
	pod.Status.Phase = corev1.PodPending
	pod.Status.Reason = "AgentAssigned"
	pod.Status.Message = "Pod assigned to agent " + agentUUID
	pod.Status.Conditions = []corev1.PodCondition{
		{
			Type:               corev1.PodScheduled,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "Scheduled",
			Message:            "Pod has been assigned to an agent",
		},
	}

	// Update stored pod with status
	p.pods.Store(getPodKey(pod), pod)

	// Update resource allocation
	p.resources.AllocatePod(pod)

	// Metrics
	podsRunning.Inc()

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

	p.agentRegistry.Range(func(uuid string, agent *AgentConnection) bool {
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

	// Metrics
	podsRunning.Dec()

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
	key := fmt.Sprintf("%s/%s", namespace, podName)

	// Get or create log stream
	val, _ := p.podLogs.LoadOrStore(key, NewPodLogStream())

	stream, ok := val.(*PodLogStream)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errInvalidLogStream, key)
	}

	return stream.Subscribe(opts.Follow), nil
}

// AppendPodLog appends a log message to the pod's log stream.
func (p *WASMProvider) AppendPodLog(namespace, name, logMsg string) {
	key := fmt.Sprintf("%s/%s", namespace, name)
	val, _ := p.podLogs.LoadOrStore(key, NewPodLogStream())

	stream, ok := val.(*PodLogStream)
	if !ok {
		klog.Errorf("unexpected log stream type for %s", key)

		return
	}

	// Ensure log message ends with newline if not present
	if !strings.HasSuffix(logMsg, "\n") {
		logMsg += "\n"
	}

	_, err := stream.Write([]byte(logMsg))
	if err != nil {
		klog.Errorf("failed to append log for %s: %v", key, err)
	}
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
	// Store the callback for use when node status changes
	p.mu.Lock()
	p.nodeStatusNotify = callback
	p.mu.Unlock()

	// Call it immediately with the current node status
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
				"kubernetes.io/hostname": p.nodeName,
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
					Address: p.nodeIP,
				},
				{
					Type:    corev1.NodeHostName,
					Address: p.nodeName,
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

// AgentPodStatus represents the status payload reported by browser agents.
type AgentPodStatus struct {
	Phase   string `json:"phase"`
	Message string `json:"message"`
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

		// Always set WASM variant to wasm32/wasi for all workloads
		// This assumes all WASM images are packaged with wasm-pack in the standard format
		agentContainer.Wasm = &struct {
			Path    string `json:"path,omitempty"`
			Variant string `json:"variant,omitempty"`
			Image   string `json:"image,omitempty"`
		}{
			Variant: "wasm32/wasi",
		}

		agentSpec.Spec.Containers = append(agentSpec.Spec.Containers, agentContainer)
	}

	return agentSpec, nil
}

// UpdatePodStatus applies a status update coming from a browser agent.
func (p *WASMProvider) UpdatePodStatus(namespace, name string, status AgentPodStatus) error {
	podKey := namespace + "/" + name

	podVal, ok := p.pods.Load(podKey)
	if !ok {
		return errdefs.NotFound("pod not found")
	}

	pod, ok := podVal.(*corev1.Pod)
	if !ok {
		return errdefs.NotFound("invalid pod type")
	}

	phase, err := agentPhaseFromString(status.Phase)
	if err != nil {
		return errdefs.AsInvalidInput(err)
	}

	updatedPod := pod.DeepCopy()
	applyAgentStatusToPod(updatedPod, phase, status.Message)

	// Store updated pod state for future queries/status calls
	p.pods.Store(podKey, updatedPod)

	// Free up agent capacity once the workload is finished
	if phase == corev1.PodSucceeded || phase == corev1.PodFailed {
		p.releasePodAllocation(podKey)
	}

	if p.notifyFunc != nil {
		p.notifyFunc(updatedPod.DeepCopy())
	}

	return nil
}

func agentPhaseFromString(phase string) (corev1.PodPhase, error) {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "pending":
		return corev1.PodPending, nil
	case "running":
		return corev1.PodRunning, nil
	case "succeeded", "success", "completed", "complete":
		return corev1.PodSucceeded, nil
	case "failed", "failure", "error":
		return corev1.PodFailed, nil
	default:
		return "", fmt.Errorf("%w: %s", errUnknownAgentPodPhase, phase)
	}
}

func applyAgentStatusToPod(pod *corev1.Pod, phase corev1.PodPhase, message string) {
	now := metav1.Now()
	pod.Status.Phase = phase
	pod.Status.Reason = "Agent" + string(phase)
	pod.Status.Message = message

	if (phase == corev1.PodRunning || phase == corev1.PodSucceeded || phase == corev1.PodFailed) && pod.Status.StartTime == nil {
		pod.Status.StartTime = &now
	}

	pod.Status.ContainerStatuses = buildContainerStatuses(pod, phase, message, now)
	pod.Status.Conditions = updatePodConditions(pod.Status.Conditions, phase, now, message)
}

func buildContainerStatuses(
	pod *corev1.Pod,
	phase corev1.PodPhase,
	message string,
	timestamp metav1.Time,
) []corev1.ContainerStatus {
	statuses := make([]corev1.ContainerStatus, len(pod.Spec.Containers))

	for i, container := range pod.Spec.Containers {
		status := corev1.ContainerStatus{
			Name:  container.Name,
			Image: container.Image,
		}

		switch phase {
		case corev1.PodPending:
			status.State.Waiting = &corev1.ContainerStateWaiting{
				Reason:  "WaitingForExecution",
				Message: message,
			}
		case corev1.PodUnknown:
			status.State.Waiting = &corev1.ContainerStateWaiting{
				Reason:  "AgentUnknown",
				Message: message,
			}
		case corev1.PodRunning:
			status.Ready = true
			status.Started = ptrTo(true)
			status.State.Running = &corev1.ContainerStateRunning{
				StartedAt: timestamp,
			}
		case corev1.PodSucceeded:
			status.Ready = true
			status.Started = ptrTo(true)
			status.State.Terminated = &corev1.ContainerStateTerminated{
				Reason:     "Completed",
				Message:    message,
				ExitCode:   0,
				FinishedAt: timestamp,
			}
		case corev1.PodFailed:
			status.State.Terminated = &corev1.ContainerStateTerminated{
				Reason:     "Error",
				Message:    message,
				ExitCode:   1,
				FinishedAt: timestamp,
			}
		}

		statuses[i] = status
	}

	return statuses
}

func updatePodConditions(
	existing []corev1.PodCondition,
	phase corev1.PodPhase,
	timestamp metav1.Time,
	message string,
) []corev1.PodCondition {
	conditions := make([]corev1.PodCondition, len(existing))
	copy(conditions, existing)

	scheduled := corev1.PodCondition{
		Type:               corev1.PodScheduled,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: timestamp,
		Reason:             "AgentScheduled",
		Message:            "Pod scheduled onto Kuack virtual node",
	}
	conditions = upsertCondition(conditions, scheduled)

	switch phase {
	case corev1.PodRunning:
		ready := corev1.PodCondition{
			Type:               corev1.PodReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: timestamp,
			Reason:             "AgentRunning",
			Message:            message,
		}
		containersReady := corev1.PodCondition{
			Type:               corev1.ContainersReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: timestamp,
			Reason:             "AgentRunning",
			Message:            message,
		}
		conditions = upsertCondition(conditions, ready)
		conditions = upsertCondition(conditions, containersReady)
	case corev1.PodPending:
		waiting := corev1.PodCondition{
			Type:               corev1.PodReady,
			Status:             corev1.ConditionFalse,
			LastTransitionTime: timestamp,
			Reason:             "WaitingForAgent",
			Message:            message,
		}
		conditions = upsertCondition(conditions, waiting)
	case corev1.PodUnknown:
		unknown := corev1.PodCondition{
			Type:               corev1.PodReady,
			Status:             corev1.ConditionFalse,
			LastTransitionTime: timestamp,
			Reason:             "AgentUnknown",
			Message:            message,
		}
		conditions = upsertCondition(conditions, unknown)
	case corev1.PodSucceeded:
		complete := corev1.PodCondition{
			Type:               corev1.PodReady,
			Status:             corev1.ConditionFalse,
			LastTransitionTime: timestamp,
			Reason:             "Completed",
			Message:            message,
		}
		conditions = upsertCondition(conditions, complete)
	case corev1.PodFailed:
		failed := corev1.PodCondition{
			Type:               corev1.PodReady,
			Status:             corev1.ConditionFalse,
			LastTransitionTime: timestamp,
			Reason:             "Failed",
			Message:            message,
		}
		conditions = upsertCondition(conditions, failed)
	}

	return conditions
}

func upsertCondition(conditions []corev1.PodCondition, condition corev1.PodCondition) []corev1.PodCondition {
	for i := range conditions {
		if conditions[i].Type == condition.Type {
			conditions[i] = condition

			return conditions
		}
	}

	return append(conditions, condition)
}

func (p *WASMProvider) releasePodAllocation(podKey string) {
	p.agentRegistry.Range(func(_ string, agent *AgentConnection) bool {
		agent.mu.Lock()

		if _, exists := agent.AllocatedPods[podKey]; exists {
			delete(agent.AllocatedPods, podKey)
			agent.mu.Unlock()
			klog.V(logVerboseLevel).Infof("Released pod %s from agent %s", podKey, agent.UUID)

			return false
		}

		agent.mu.Unlock()

		return true
	})
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

	p.agentRegistry.Range(func(uuid string, agent *AgentConnection) bool {
		// Skip throttled agents
		if agent.IsThrottled {
			klog.V(logVerboseLevel).Infof("Agent %s is throttled, skipping", uuid)

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

			klog.V(logVerboseLevel).Infof("Agent %s available CPU: %s", uuid, availableCPU.String())

			// Select agent with most available CPU
			if availableCPU.Cmp(maxAvailable) >= 0 {
				maxAvailable = availableCPU
				selectedAgent = uuid
			}
		} else {
			klog.V(logVerboseLevel).Infof("Agent %s has insufficient capacity", uuid)
		}

		return true
	})

	if selectedAgent == "" {
		klog.Warningf("No suitable agent found. Agents count: %d", p.agentsCount())

		return "", ErrNoSuitableAgent
	}

	return selectedAgent, nil
}

func ptrTo[T any](value T) *T {
	return &value
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

	// Set write deadline
	err = agent.Stream.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err != nil {
		klog.Errorf("Failed to set write deadline: %v", err)

		return err
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
	err = agent.Stream.WriteJSON(msg)
	if err != nil {
		return err
	}

	klog.V(logVerboseLevel).Infof("Successfully sent pod spec to agent %s", agent.UUID)

	return nil
}

func (p *WASMProvider) deletePodFromAgent(agent *AgentConnection, pod *corev1.Pod) error {
	deleteData := struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	}{
		Namespace: pod.Namespace,
		Name:      pod.Name,
	}

	data, err := json.Marshal(deleteData)
	if err != nil {
		return fmt.Errorf("failed to marshal delete data: %w", err)
	}

	// Set write deadline
	err = agent.Stream.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err != nil {
		klog.Errorf("Failed to set write deadline: %v", err)

		return err
	}

	msg := struct {
		Type      string          `json:"type"`
		Timestamp time.Time       `json:"timestamp"`
		Data      json.RawMessage `json:"data"`
	}{
		Type:      "pod_delete",
		Timestamp: time.Now(),
		Data:      json.RawMessage(data),
	}

	// Send message via WebSocket
	// Note: WriteJSON is not thread-safe. We rely on the fact that CreatePod/DeletePod
	// are serialized for the same pod, but for the same agent they might overlap.
	// Ideally we should lock the connection.
	// agent.mu.Lock() // This might deadlock if held elsewhere?
	// defer agent.mu.Unlock()

	err = agent.Stream.WriteJSON(msg)
	if err != nil {
		return fmt.Errorf("failed to send delete command: %w", err)
	}

	klog.V(logVerboseLevel).Infof("Sent delete command for pod %s to agent %s", getPodKey(pod), agent.UUID)

	return nil
}

func (p *WASMProvider) agentsCount() int {
	count := 0

	p.agentRegistry.Range(func(_ string, _ *AgentConnection) bool {
		count++

		return true
	})

	return count
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

	cpuCapacity := rt.totalCapacity[corev1.ResourceCPU]
	cpuAllocated := rt.totalAllocated[corev1.ResourceCPU]
	cpuAvail := cpuCapacity.DeepCopy()
	cpuAvail.Sub(cpuAllocated)
	// Ensure non-negative
	if cpuAvail.Sign() < 0 {
		cpuAvail = resource.MustParse("0")
	}

	memCapacity := rt.totalCapacity[corev1.ResourceMemory]
	memAllocated := rt.totalAllocated[corev1.ResourceMemory]
	memAvail := memCapacity.DeepCopy()
	memAvail.Sub(memAllocated)
	// Ensure non-negative
	if memAvail.Sign() < 0 {
		memAvail = resource.MustParse("0")
	}

	klog.Infof("[Resources] Allocatable: CPU=%s (capacity=%s, allocated=%s), Memory=%s (capacity=%s, allocated=%s)",
		(&cpuAvail).String(),
		(&cpuCapacity).String(),
		(&cpuAllocated).String(),
		(&memAvail).String(),
		(&memCapacity).String(),
		(&memAllocated).String())

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

	klog.Infof("[Resources] AddAgent %s: CPU capacity now %s, Memory capacity now %s", uuid, cpu.String(), mem.String())
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
