package provider

import (
	"github.com/prometheus/client_golang/prometheus"
)

//nolint:gochecknoglobals // Prometheus mandates global collector singletons so the registry can find them.
var (
	agentsConnected = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vk_wasm_agents_connected",
		Help: "Number of connected browser agents",
	})

	podsRunning = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vk_wasm_pods_running",
		Help: "Number of running pods on WASM agents",
	})

	cpuCapacity = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vk_wasm_cpu_capacity_cores",
		Help: "Total CPU capacity provided by agents in cores",
	})

	memoryCapacity = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vk_wasm_memory_capacity_bytes",
		Help: "Total memory capacity provided by agents in bytes",
	})
)

//nolint:gochecknoinits // registration has to happen in init() so collectors exist before any handler starts.
func init() {
	prometheus.MustRegister(agentsConnected)
	prometheus.MustRegister(podsRunning)
	prometheus.MustRegister(cpuCapacity)
	prometheus.MustRegister(memoryCapacity)
}
