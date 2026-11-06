# microkind — Kubernetes in Firecracker MicroVMs

`microkind` (formerly `fckind`) is a fast, ultra-minimal, kind-compatible Kubernetes cluster orchestrator that provisions multi-node upstream Kubernetes clusters inside lightweight **Firecracker microVMs** running on **Alpine Linux** and powered by **`k0s`**.

## Features

- **Blazing Fast Boot Times**: Boots multi-node Kubernetes clusters inside Firecracker microVMs with custom minimal Linux 6.1 kernels in under 2.5 seconds.
- **Ultra-lightweight Underlay**: Alpine Linux rootfs + per-node sparse writable overlay (`overlayfs` over `/dev/vdb`) with native ext4 containerd bind mounts.
- **100% Offline Airgap Support**: Pre-baked `k0s` airgap image bundle (`cni-node`, `kube-router`, `coredns`, `kube-proxy`, `metrics-server`, `pause`) for instant bootstrapping with zero internet dependency.
- **Certified Upstream Kubernetes**: Powered by `k0s` with embedded `kine` (SQLite for single control-plane) or embedded `etcd` (multi-master HA).
- **Zero-SSH Guest Control**: Host communicates with guest nodes exclusively over `virtio-vsock` (Port 1024) via `microkind-agent`.
- **Declarative YAML Configuration**: Declarative `cluster.yaml` format allowing custom CNI (Kube-Router, Calico), CRI (containerd), vCPUs, and memory limits per node.
- **Kind-compatible CLI**: Familiar commands: `microkind create cluster`, `microkind get nodes`, `microkind load docker-image`, `microkind exec`, and `microkind delete cluster`.

## Architecture

```
Host / Development VM (User Space)
  ├── microkind CLI (Go)
  ├── Host Bridge (fcbr-<hash>) + TAP devices (fctap-<node>)
  └── Firecracker REST API & virtio-vsock sockets

MicroVMs (Control Plane & Workers)
  ├── Custom Linux 6.1 Kernel (vmlinux with netfilter & IP_SET)
  ├── /sbin/overlay-init (PID 1: mounts RO /dev/vda + RW /dev/vdb overlay)
  ├── /usr/local/bin/microkind-agent (AF_VSOCK RPC daemon on port 1024)
  └── k0s (Kubernetes control plane / worker daemons)
```

## Quick Start

### Build Binaries

```bash
# Build CLI
go build -o bin/microkind ./cmd/fckind

# Build static guest init & agent
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/fckind-guest-init ./cmd/fckind-guest-init
```

### Build Node Image

```bash
microkind build node-image
```

### Create Cluster

```bash
microkind create cluster --name dev
```

### Inspect and Use

```bash
export KUBECONFIG=~/.local/share/fckind/clusters/dev/kubeconfig
kubectl get nodes -o wide
microkind exec dev-control-plane -- uname -a
microkind load docker-image my-app:latest
```

### Delete Cluster

```bash
microkind delete cluster --name dev
```
