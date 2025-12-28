package provider

//nolint:gochecknoglobals // expose private metrics so black-box tests can assert registrations without altering prod code
var (
	AgentsConnected = agentsConnected
	PodsRunning     = podsRunning
	CpuCapacity     = cpuCapacity
	MemoryCapacity  = memoryCapacity
)
