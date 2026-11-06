package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"fckind/pkg/config"
	"fckind/pkg/image"
	"fckind/pkg/network"
	"fckind/pkg/vmm"
	"fckind/pkg/vsock"
)

// ClusterState holds the persistent metadata for a provisioned cluster
type ClusterState struct {
	Config     config.ClusterConfig `json:"config"`
	BridgeName string               `json:"bridge_name"`
	GatewayIP  string               `json:"gateway_ip"`
	SubnetCIDR string               `json:"subnet_cidr"`
	Nodes      map[string]NodeState `json:"nodes"`
	CreatedAt  time.Time            `json:"created_at"`
}

// NodeState holds metadata for an individual microVM node
type NodeState struct {
	Name        string          `json:"name"`
	Role        config.NodeRole `json:"role"`
	GuestIP     string          `json:"guest_ip"`
	TAPName     string          `json:"tap_name"`
	GuestCID    uint32          `json:"guest_cid"`
	VsockPath   string          `json:"vsock_path"`
	APISockPath string          `json:"api_sock_path"`
	StateDir    string          `json:"state_dir"`
}

// Manager orchestrates cluster lifecycle
type Manager struct {
	DataDir string
}

// NewManager creates a new cluster manager
func NewManager() (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/home/ubuntu"
	}
	dataDir := filepath.Join(home, ".local", "share", "fckind", "clusters")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", dataDir, err)
	}
	return &Manager{DataDir: dataDir}, nil
}

// ClusterDir returns the state directory for a cluster
func (m *Manager) ClusterDir(name string) string {
	return filepath.Join(m.DataDir, name)
}

// CreateCluster creates and bootstraps a multi-node Firecracker K8s cluster
func (m *Manager) CreateCluster(ctx context.Context, cfg *config.ClusterConfig) error {
	clusterDir := m.ClusterDir(cfg.Name)
	if _, err := os.Stat(clusterDir); err == nil {
		return fmt.Errorf("cluster %q already exists (run 'fckind delete cluster --name %s' first)", cfg.Name, cfg.Name)
	}

	fmt.Printf("Creating cluster %q with %d nodes...\n", cfg.Name, len(cfg.Nodes))

	// 1. Verify / prepare cached node image (vmlinux + rootfs.ext4)
	builder, err := image.NewBuilder(cfg.K0s.Version, true)
	if err != nil {
		return err
	}
	cacheDir := builder.TargetCacheDir()
	rootfsPath := filepath.Join(cacheDir, "rootfs.ext4")
	kernelPath := filepath.Join(cacheDir, "vmlinux")

	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		fmt.Printf("Base node image not found in cache. Building Alpine+k0s node image...\n")
		repoRoot := "/home/ubuntu/fckind"
		if _, err := os.Stat(repoRoot); os.IsNotExist(err) {
			repoRoot = "/home/chan/fckind"
		}
		if err := builder.BuildNodeImage(ctx, repoRoot); err != nil {
			return fmt.Errorf("build node image: %w", err)
		}
	}

	// 2. Setup Host Network (Bridge + NAT)
	netMgr, err := network.NewNetworkManager(cfg.Name)
	if err != nil {
		return fmt.Errorf("init network manager: %w", err)
	}
	if err := netMgr.SetupBridge(); err != nil {
		return fmt.Errorf("setup bridge: %w", err)
	}

	state := &ClusterState{
		Config:     *cfg,
		BridgeName: netMgr.BridgeName,
		GatewayIP:  netMgr.GatewayIP.String(),
		SubnetCIDR: netMgr.Subnet.String(),
		Nodes:      make(map[string]NodeState),
		CreatedAt:  time.Now(),
	}

	if err := os.MkdirAll(clusterDir, 0755); err != nil {
		return fmt.Errorf("create cluster dir: %w", err)
	}

	// 3. Launch MicroVMs in parallel
	var instances []*vmm.Instance
	var cpNode *NodeState

	for i, nodeCfg := range cfg.Nodes {
		nodeName := fmt.Sprintf("%s-%s-%d", cfg.Name, nodeCfg.Role, i)
		if nodeCfg.Role == config.RoleControlPlane && i == 0 {
			nodeName = fmt.Sprintf("%s-control-plane", cfg.Name)
		}

		nodeDir := filepath.Join(clusterDir, nodeName)
		_ = os.MkdirAll(nodeDir, 0755)

		guestIP := netMgr.NodeIP(i)
		tapName := netMgr.TAPName(i, string(nodeCfg.Role))
		if err := netMgr.CreateTAP(tapName); err != nil {
			return fmt.Errorf("create tap for %s: %w", nodeName, err)
		}

		overlayPath := filepath.Join(nodeDir, "overlay.ext4")
		if _, err := os.Stat(overlayPath); os.IsNotExist(err) {
			_ = exec.Command("truncate", "-s", fmt.Sprintf("%dG", nodeCfg.DiskSizeGiB), overlayPath).Run()
			_ = exec.Command("mkfs.ext4", "-F", "-m", "0", overlayPath).Run()
		}

		guestCID := uint32(3 + i)
		vmmCfg := vmm.InstanceConfig{
			ID:          nodeName,
			KernelPath:  kernelPath,
			RootfsPath:  rootfsPath,
			OverlayPath: overlayPath,
			TAPDevName:  tapName,
			GuestIP:     guestIP,
			GatewayIP:   netMgr.GatewayIP.String(),
			Hostname:    nodeName,
			VCPUs:       nodeCfg.VCPUs,
			MemoryMiB:   nodeCfg.MemoryMiB,
			GuestCID:    guestCID,
			SocketDir:   nodeDir,
		}

		inst, err := vmm.NewInstance(vmmCfg)
		if err != nil {
			return fmt.Errorf("init vmm instance for %s: %w", nodeName, err)
		}

		fmt.Printf("Booting microVM for %s (IP: %s, %d vCPUs, %d MiB RAM)...\n", nodeName, guestIP, nodeCfg.VCPUs, nodeCfg.MemoryMiB)
		if err := inst.Start(ctx); err != nil {
			return fmt.Errorf("boot microVM %s: %w", nodeName, err)
		}
		instances = append(instances, inst)

		nodeState := NodeState{
			Name:        nodeName,
			Role:        nodeCfg.Role,
			GuestIP:     guestIP,
			TAPName:     tapName,
			GuestCID:    guestCID,
			VsockPath:   inst.Config.VsockPath,
			APISockPath: inst.APISock,
			StateDir:    nodeDir,
		}
		state.Nodes[nodeName] = nodeState

		if nodeCfg.Role == config.RoleControlPlane && cpNode == nil {
			cpCopy := nodeState
			cpNode = &cpCopy
		}
	}

	// Save initial state
	if err := m.saveState(cfg.Name, state); err != nil {
		return fmt.Errorf("save cluster state: %w", err)
	}

	// 4. Wait for guest agents over vsock
	fmt.Printf("Waiting for microVM guest agents to respond over virtio-vsock...\n")
	for _, n := range state.Nodes {
		cli := vsock.NewClient(n.VsockPath)
		if err := cli.WaitForReady(ctx, 15*time.Second); err != nil {
			return fmt.Errorf("node %s agent not ready: %w", n.Name, err)
		}
		// Write /etc/hosts and /etc/resolv.conf so k0s DNS & pre-flight checks pass
		_ = cli.WriteFile(ctx, "/etc/hosts", []byte(fmt.Sprintf("127.0.0.1 localhost\n::1 localhost\n%s %s\n", n.GuestIP, n.Name)), 0644)
		_ = cli.WriteFile(ctx, "/etc/resolv.conf", []byte("nameserver 1.1.1.1\nnameserver 8.8.8.8\n"), 0644)
	}

	// 5. Setup optional host port forward for API server
	if cfg.Networking.APIServerPort > 0 && cpNode != nil {
		_ = netMgr.SetupPortForward(cfg.Networking.APIServerPort, cpNode.GuestIP)
	}

	// 6. Bootstrap Control Plane via k0s
	if cpNode == nil {
		return fmt.Errorf("no control plane node defined")
	}

	fmt.Printf("Initializing Kubernetes control-plane on %s...\n", cpNode.Name)
	cpCli := vsock.NewClient(cpNode.VsockPath)

	hasWorkers := false
	for _, n := range cfg.Nodes {
		if n.Role == config.RoleWorker {
			hasWorkers = true
			break
		}
	}

	installCmd := []string{"k0s", "install", "controller", "--ignore-pre-flight-checks"}
	if !hasWorkers {
		installCmd = append(installCmd, "--single")
	} else {
		installCmd = append(installCmd, "--enable-worker=false")
	}
	if cfg.K0s.Storage.Type == "etcd" {
		installCmd = []string{"k0s", "install", "controller", "--ignore-pre-flight-checks"}
	}
	if resp, err := cpCli.Exec(ctx, installCmd, nil, "", nil); err != nil || resp.ExitCode != 0 {
		return fmt.Errorf("k0s install controller failed: %v (%s)", err, string(resp.Stderr))
	}

	if resp, err := cpCli.Exec(ctx, []string{"rc-service", "k0scontroller", "start"}, nil, "", nil); err != nil || resp.ExitCode != 0 {
		// Fallback to k0s start directly
		_, _ = cpCli.Exec(ctx, []string{"k0s", "start"}, nil, "", nil)
	}

	// Poll until k0s is ready
	fmt.Printf("Waiting for Kubernetes API server to become ready...\n")
	var kubeconfigBytes []byte
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := cpCli.Exec(ctx, []string{"k0s", "kubeconfig", "create", "--groups", "system:masters", "admin"}, nil, "", nil)
		if err == nil && resp.ExitCode == 0 && len(resp.Stdout) > 0 {
			kubeconfigBytes = resp.Stdout
			break
		}
		time.Sleep(2 * time.Second)
	}

	if len(kubeconfigBytes) == 0 {
		return fmt.Errorf("timed out waiting for k0s kubeconfig")
	}

	// Fix kubeconfig server URL: replace localhost with bridge IP or port forward
	serverTarget := fmt.Sprintf("https://%s:6443", cpNode.GuestIP)
	if cfg.Networking.APIServerPort > 0 {
		serverTarget = fmt.Sprintf("https://127.0.0.1:%d", cfg.Networking.APIServerPort)
	}
	fixedKubeconfig := strings.ReplaceAll(string(kubeconfigBytes), "https://localhost:6443", serverTarget)
	fixedKubeconfig = strings.ReplaceAll(fixedKubeconfig, "https://127.0.0.1:6443", serverTarget)

	kubeconfigPath := filepath.Join(clusterDir, "kubeconfig")
	if err := os.WriteFile(kubeconfigPath, []byte(fixedKubeconfig), 0600); err != nil {
		return fmt.Errorf("write cluster kubeconfig: %w", err)
	}

	// 7. Generate Worker Token & Join Workers in Parallel
	var workerToken string
	tokenDeadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(tokenDeadline) {
		workerTokenResp, err := cpCli.Exec(ctx, []string{"k0s", "token", "create", "--role=worker", "--expiry=2h"}, nil, "", nil)
		if err == nil && workerTokenResp.ExitCode == 0 && len(workerTokenResp.Stdout) > 0 {
			workerToken = strings.TrimSpace(string(workerTokenResp.Stdout))
			break
		}
		time.Sleep(2 * time.Second)
	}
	if workerToken == "" {
		return fmt.Errorf("generate worker token timed out waiting for status.sock")
	}

	for _, n := range state.Nodes {
		if n.Role != config.RoleWorker {
			continue
		}
		fmt.Printf("Joining worker %s to cluster...\n", n.Name)
		wCli := vsock.NewClient(n.VsockPath)
		// Write token to guest /etc/k0s/worker-token
		if err := wCli.WriteFile(ctx, "/etc/k0s/worker-token", []byte(workerToken), 0600); err != nil {
			return fmt.Errorf("write worker token to %s: %w", n.Name, err)
		}
		// Install & start worker
		joinCmd := []string{"k0s", "install", "worker", "--ignore-pre-flight-checks", "--token-file", "/etc/k0s/worker-token"}
		if resp, err := wCli.Exec(ctx, joinCmd, nil, "", nil); err != nil || resp.ExitCode != 0 {
			return fmt.Errorf("k0s install worker on %s failed: %v (%s)", n.Name, err, string(resp.Stderr))
		}
		_, _ = wCli.Exec(ctx, []string{"rc-service", "k0sworker", "start"}, nil, "", nil)
	}

	fmt.Printf("\nCluster %q created successfully!\n", cfg.Name)
	fmt.Printf("To use your cluster with kubectl:\n")
	fmt.Printf("  export KUBECONFIG=%s\n", kubeconfigPath)
	fmt.Printf("  kubectl get nodes -o wide\n")

	return nil
}

// DeleteCluster tears down all microVMs, cleans up networking, and removes state
func (m *Manager) DeleteCluster(ctx context.Context, name string) error {
	state, err := m.GetCluster(name)
	if err != nil {
		return err
	}

	fmt.Printf("Deleting cluster %q...\n", name)

	// 1. Kill all Firecracker instances
	for _, n := range state.Nodes {
		_ = exec.Command("pkill", "-f", n.Name).Run()
	}

	// 2. Clean up network
	netMgr, err := network.NewNetworkManager(name)
	if err == nil {
		var tapNames []string
		for _, n := range state.Nodes {
			tapNames = append(tapNames, n.TAPName)
		}
		_ = netMgr.Teardown(tapNames)
	}

	// 3. Remove cluster state dir
	clusterDir := m.ClusterDir(name)
	_ = os.RemoveAll(clusterDir)

	fmt.Printf("Cluster %q deleted successfully.\n", name)
	return nil
}

// GetCluster loads the cluster state from disk
func (m *Manager) GetCluster(name string) (*ClusterState, error) {
	statePath := filepath.Join(m.ClusterDir(name), "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("cluster %q not found: %w", name, err)
	}
	var state ClusterState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}
	return &state, nil
}

// ListClusters returns all provisioned cluster names
func (m *Manager) ListClusters() ([]string, error) {
	entries, err := os.ReadDir(m.DataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// LoadDockerImage streams an image tarball into all nodes in the cluster
func (m *Manager) LoadDockerImage(ctx context.Context, clusterName string, imageName string) error {
	state, err := m.GetCluster(clusterName)
	if err != nil {
		return err
	}

	fmt.Printf("Saving docker image %s...\n", imageName)
	saveCmd := exec.CommandContext(ctx, "docker", "save", imageName)
	pipe, err := saveCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("docker save pipe: %w", err)
	}
	if err := saveCmd.Start(); err != nil {
		return fmt.Errorf("start docker save: %w", err)
	}

	// Read into memory or stream to each node
	tarBytes, err := io.ReadAll(pipe)
	if err != nil {
		return fmt.Errorf("read docker save: %w", err)
	}
	if err := saveCmd.Wait(); err != nil {
		return fmt.Errorf("wait docker save: %w", err)
	}

	for _, n := range state.Nodes {
		fmt.Printf("Loading %s into node %s...\n", imageName, n.Name)
		cli := vsock.NewClient(n.VsockPath)
		// Import via k0s containerd
		resp, err := cli.Exec(ctx, []string{"k0s", "ctr", "images", "import", "-"}, nil, "", tarBytes)
		if err != nil || resp.ExitCode != 0 {
			return fmt.Errorf("import image into %s: %v (%s)", n.Name, err, string(resp.Stderr))
		}
	}

	fmt.Printf("Image %s loaded into all nodes successfully.\n", imageName)
	return nil
}

func (m *Manager) saveState(name string, state *ClusterState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(m.ClusterDir(name), "state.json")
	return os.WriteFile(path, data, 0644)
}
