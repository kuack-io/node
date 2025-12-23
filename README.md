# Kuack Node

Kuack Node is a Virtual Kubelet provider for WASM (WebAssembly) workloads that enables scheduling Kubernetes pods to browser-based agents.

For comprehensive documentation, please visit [kuack.io](https://kuack.io) or the main [Kuack](https://github.com/kuack-io/kuack) repository.

## Overview

Kuack Node implements the Virtual Kubelet interface to:
- Register a virtual node in your Kubernetes cluster
- Accept pods and schedule them to browser-based WASM agents
- Communicate with agents using HTTP/WebSocket protocol
- Manage pod lifecycle and resource allocation

## Deployment

The application is designed to be deployed on Kubernetes using Helm.
Please refer to the [Kuack Helm Charts](https://github.com/kuack-io/helm) repository for deployment instructions.

## Configuration

The application reads configuration from environment variables:

- `NODE_NAME` - Name of the virtual node (default: `wasm-node`)
- `HTTP_LISTEN_ADDR` - HTTP server listen address (default: `:8080`)
- `KUBECONFIG` - Path to kubeconfig file (optional, uses in-cluster config if not set)
- `KLOG_VERBOSITY` - klog verbosity level (default: `2`)

## Protocol

The application communicates with browser-based agents using HTTP (for initial connection and health checks) and WebSocket (for agent registration and pod management).

TLS termination should be handled at the ingress controller level. The application serves plain HTTP and expects to be behind an ingress that terminates TLS.
