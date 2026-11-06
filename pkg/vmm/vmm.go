package vmm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// InstanceConfig contains configuration to launch a Firecracker microVM
type InstanceConfig struct {
	ID              string
	KernelPath      string
	RootfsPath      string
	OverlayPath     string
	TAPDevName      string
	GuestIP         string
	GatewayIP       string
	Netmask         string
	Hostname        string
	VCPUs           int
	MemoryMiB       int
	GuestCID        uint32
	VsockPath       string
	SocketDir       string
	ExtraKernelArgs string
}

// Instance manages the lifecycle and REST client for a single Firecracker microVM
type Instance struct {
	Config   InstanceConfig
	Cmd      *exec.Cmd
	APISock  string
	HTTPCli  *http.Client
	Stdout   *os.File
	Stderr   *os.File
}

// NewInstance creates a new microVM instance definition
func NewInstance(cfg InstanceConfig) (*Instance, error) {
	if cfg.SocketDir == "" {
		cfg.SocketDir = filepath.Join(os.TempDir(), "fckind", cfg.ID)
	}
	if err := os.MkdirAll(cfg.SocketDir, 0755); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}

	apiSock := filepath.Join(cfg.SocketDir, "api.sock")
	if cfg.VsockPath == "" {
		cfg.VsockPath = filepath.Join(cfg.SocketDir, "vsock.sock")
	}

	httpCli := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", apiSock)
			},
		},
		Timeout: 5 * time.Second,
	}

	return &Instance{
		Config:  cfg,
		APISock: apiSock,
		HTTPCli: httpCli,
	}, nil
}

// Start launches the firecracker binary and configures the microVM via the REST API
func (inst *Instance) Start(ctx context.Context) error {
	// Clean up stale socket if present
	_ = os.Remove(inst.APISock)
	_ = os.Remove(inst.Config.VsockPath)

	logPath := filepath.Join(inst.Config.SocketDir, "firecracker.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open vm log file: %w", err)
	}

	// 1. Spawn Firecracker daemon
	cmd := exec.CommandContext(ctx, "firecracker", "--api-sock", inst.APISock)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start firecracker binary: %w", err)
	}
	inst.Cmd = cmd

	// 2. Wait for API socket to be created and respond
	if err := inst.waitForAPISocket(ctx, 3*time.Second); err != nil {
		inst.Kill()
		return fmt.Errorf("wait for firecracker api socket: %w", err)
	}

	// 3. Configure Machine (vCPUs, Memory)
	machineCfg := map[string]any{
		"vcpu_count":   inst.Config.VCPUs,
		"mem_size_mib": inst.Config.MemoryMiB,
		"smt":          false,
	}
	if err := inst.put(ctx, "/machine-config", machineCfg); err != nil {
		inst.Kill()
		return fmt.Errorf("put /machine-config: %w", err)
	}

	// 4. Configure Boot Source (Kernel + Cmdline)
	netmask := inst.Config.Netmask
	if netmask == "" {
		netmask = "255.255.255.0"
	}
	// Format: ip=<client-ip>:<server-ip>:<gw-ip>:<netmask>:<hostname>:<device>:<autoconf>
	kernelCmdline := fmt.Sprintf(
		"console=ttyS0 reboot=k panic=1 pci=off ip=%s::%s:%s:%s:eth0:off init=/sbin/overlay-init %s",
		inst.Config.GuestIP,
		inst.Config.GatewayIP,
		netmask,
		inst.Config.Hostname,
		inst.Config.ExtraKernelArgs,
	)
	bootSource := map[string]any{
		"kernel_image_path": inst.Config.KernelPath,
		"boot_args":         strings.TrimSpace(kernelCmdline),
	}
	if err := inst.put(ctx, "/boot-source", bootSource); err != nil {
		inst.Kill()
		return fmt.Errorf("put /boot-source: %w", err)
	}

	// 5. Configure Read-Only Base Rootfs Drive (/dev/vda)
	rootfsDrive := map[string]any{
		"drive_id":       "rootfs",
		"path_on_host":   inst.Config.RootfsPath,
		"is_root_device": true,
		"is_read_only":   true,
	}
	if err := inst.put(ctx, "/drives/rootfs", rootfsDrive); err != nil {
		inst.Kill()
		return fmt.Errorf("put /drives/rootfs: %w", err)
	}

	// 6. Configure Per-Node Writable Overlay Drive (/dev/vdb)
	if inst.Config.OverlayPath != "" {
		overlayDrive := map[string]any{
			"drive_id":       "overlay",
			"path_on_host":   inst.Config.OverlayPath,
			"is_root_device": false,
			"is_read_only":   false,
		}
		if err := inst.put(ctx, "/drives/overlay", overlayDrive); err != nil {
			inst.Kill()
			return fmt.Errorf("put /drives/overlay: %w", err)
		}
	}

	// 7. Configure Network Interface (TAP)
	if inst.Config.TAPDevName != "" {
		netIface := map[string]any{
			"iface_id":      "eth0",
			"host_dev_name": inst.Config.TAPDevName,
		}
		if err := inst.put(ctx, "/network-interfaces/eth0", netIface); err != nil {
			inst.Kill()
			return fmt.Errorf("put /network-interfaces/eth0: %w", err)
		}
	}

	// 8. Configure Virtio-Vsock
	if inst.Config.GuestCID > 0 {
		vsockCfg := map[string]any{
			"vsock_id":  "vsock",
			"guest_cid": inst.Config.GuestCID,
			"uds_path":  inst.Config.VsockPath,
		}
		if err := inst.put(ctx, "/vsock", vsockCfg); err != nil {
			inst.Kill()
			return fmt.Errorf("put /vsock: %w", err)
		}
	}

	// 9. Start Instance
	startAction := map[string]any{
		"action_type": "InstanceStart",
	}
	if err := inst.put(ctx, "/actions", startAction); err != nil {
		inst.Kill()
		return fmt.Errorf("put /actions InstanceStart: %w", err)
	}

	return nil
}

// Pause pauses the microVM vCPUs
func (inst *Instance) Pause(ctx context.Context) error {
	return inst.put(ctx, "/actions", map[string]any{"action_type": "Pause"})
}

// Resume resumes the microVM vCPUs
func (inst *Instance) Resume(ctx context.Context) error {
	return inst.put(ctx, "/actions", map[string]any{"action_type": "Resume"})
}

// Kill terminates the microVM process and removes temporary sockets
func (inst *Instance) Kill() {
	if inst.Cmd != nil && inst.Cmd.Process != nil {
		_ = inst.Cmd.Process.Kill()
		_ = inst.Cmd.Wait()
	}
	_ = os.Remove(inst.APISock)
	_ = os.Remove(inst.Config.VsockPath)
}

func (inst *Instance) waitForAPISocket(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if _, err := os.Stat(inst.APISock); err == nil {
			// Try a test GET /
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/", nil)
			if resp, err := inst.HTTPCli.Do(req); err == nil {
				resp.Body.Close()
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for api socket %s", inst.APISock)
}

func (inst *Instance) put(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal put body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost"+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create put request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := inst.HTTPCli.Do(req)
	if err != nil {
		return fmt.Errorf("do put request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("api error (%d %s): %s", resp.StatusCode, resp.Status, string(respBody))
	}

	return nil
}
