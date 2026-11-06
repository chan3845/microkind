# fckind — Kubernetes in Firecracker MicroVMs

`fckind` (Firecracker KIND) is a fast, ultra-minimal, kind-compatible Kubernetes cluster orchestrator that provisions multi-node upstream Kubernetes clusters inside lightweight **Firecracker microVMs** running on **Alpine Linux** and powered by **`k0s`**.

## Features

- **Blazing Fast Boot Times**: Boots multi-node Kubernetes clusters inside Firecracker microVMs with custom minimal Linux 6.1 kernels.
- **Ultra-lightweight Underlay**: Alpine Linux rootfs (~220 MB read-only base) + per-node sparse writable overlay (`overlayfs` over `/dev/vdb`).
- **Certified Upstream Kubernetes**: Powered by `k0s` with embedded `kine` (SQLite for single control-plane) or embedded `etcd` (multi-master HA).
- **Zero-SSH Guest Control**: Host communicates with guest nodes exclusively over `virtio-vsock` (Port 1024) via `fckind-agent`.
- **Declarative YAML Configuration**: Declarative `cluster.yaml` format allowing custom CNI (Kube-Router, Calico), CRI (containerd), vCPUs, and memory limits per node.
- **Kind-compatible CLI**: Familiar commands: `fckind create cluster`, `fckind get nodes`, `fckind load docker-image`, `fckind exec`, and `fckind delete cluster`.

## Architecture

```
Host / Development VM (User Space)
  ├── fckind CLI (Go)
  ├── Host Bridge (fcbr-<hash>) + TAP devices (fctap-<node>)
  └── Firecracker REST API & virtio-vsock sockets

MicroVMs (Control Plane & Workers)
  ├── Custom Linux 6.1 Kernel (vmlinux)
  ├── /sbin/overlay-init (PID 1: mounts RO /dev/vda + RW /dev/vdb overlay)
  ├── /usr/local/bin/fckind-agent (AF_VSOCK RPC daemon on port 1024)
  └── k0s (Kubernetes control plane / worker daemons)
```

## Quick Start

### Build Binaries

```bash
# Build fckind CLI
go build -o bin/fckind ./cmd/fckind

# Build static guest init & agent
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/fckind-guest-init ./cmd/fckind-guest-init
```

### Build Node Image

```bash
fckind build node-image
```

### Create Cluster

```bash
fckind create cluster --name dev
```

### Inspect and Use

```bash
export KUBECONFIG=~/.local/share/fckind/clusters/dev/kubeconfig
kubectl get nodes -o wide
fckind exec dev-control-plane -- uname -a
fckind load docker-image my-app:latest
```

### Delete Cluster

```bash
fckind delete cluster --name dev
```
