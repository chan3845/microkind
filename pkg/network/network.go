package network

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// NetworkManager manages host Linux bridge, TAP interfaces, and NAT rules
type NetworkManager struct {
	ClusterName string
	BridgeName  string
	Subnet      *net.IPNet
	GatewayIP   net.IP
}

// NewNetworkManager creates a network manager with a deterministic subnet for the cluster
func NewNetworkManager(clusterName string) (*NetworkManager, error) {
	// Generate deterministic short bridge name (max 15 chars for Linux IFNAMSIZ)
	hash := sha256.Sum256([]byte(clusterName))
	shortHash := hex.EncodeToString(hash[:])[:6]
	bridgeName := fmt.Sprintf("fcbr-%s", shortHash)

	// Deterministic /24 subnet in 172.30.X.0/24 range
	octet := int(hash[0]) % 250 // 0 to 249
	_, subnet, err := net.ParseCIDR(fmt.Sprintf("172.30.%d.0/24", octet))
	if err != nil {
		return nil, fmt.Errorf("generate subnet: %w", err)
	}

	gatewayIP := net.ParseIP(fmt.Sprintf("172.30.%d.1", octet))

	return &NetworkManager{
		ClusterName: clusterName,
		BridgeName:  bridgeName,
		Subnet:      subnet,
		GatewayIP:   gatewayIP,
	}, nil
}

// NodeIP returns the deterministic IPv4 address for a node index (0 -> .2, 1 -> .3, etc.)
func (m *NetworkManager) NodeIP(nodeIndex int) string {
	ipBytes := m.GatewayIP.To4()
	return fmt.Sprintf("%d.%d.%d.%d", ipBytes[0], ipBytes[1], ipBytes[2], 2+nodeIndex)
}

// TAPName returns a valid interface name for a node (<= 15 chars)
func (m *NetworkManager) TAPName(nodeIndex int, role string) string {
	hash := sha256.Sum256([]byte(m.ClusterName))
	prefix := "cp"
	if strings.Contains(strings.ToLower(role), "worker") {
		prefix = "wk"
	}
	return fmt.Sprintf("fct%s%x-%d", prefix, hash[0], nodeIndex)
}

// SetupBridge creates the bridge interface, assigns the gateway IP, enables forwarding, and sets up NAT
func (m *NetworkManager) SetupBridge() error {
	// 1. Create bridge if not already present
	if !interfaceExists(m.BridgeName) {
		if out, err := exec.Command("sudo", "ip", "link", "add", m.BridgeName, "type", "bridge").CombinedOutput(); err != nil {
			return fmt.Errorf("create bridge %s: %w (%s)", m.BridgeName, err, string(out))
		}
	}

	// 2. Assign Gateway IP
	gwCIDR := fmt.Sprintf("%s/24", m.GatewayIP.String())
	_ = exec.Command("sudo", "ip", "addr", "add", gwCIDR, "dev", m.BridgeName).Run()

	// 3. Bring bridge UP
	if out, err := exec.Command("sudo", "ip", "link", "set", m.BridgeName, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("bring up bridge %s: %w (%s)", m.BridgeName, err, string(out))
	}

	// 4. Enable IPv4 forwarding
	if out, err := exec.Command("sudo", "sysctl", "-w", "net.ipv4.ip_forward=1").CombinedOutput(); err != nil {
		return fmt.Errorf("enable ip forwarding: %w (%s)", err, string(out))
	}

	// 5. Add iptables NAT MASQUERADE rule for outbound microVM traffic
	subnetStr := m.Subnet.String()
	_ = exec.Command("sudo", "iptables", "-t", "nat", "-C", "POSTROUTING", "-s", subnetStr, "!", "-o", m.BridgeName, "-j", "MASQUERADE").Run()
	if out, err := exec.Command("sudo", "iptables", "-t", "nat", "-A", "POSTROUTING", "-s", subnetStr, "!", "-o", m.BridgeName, "-j", "MASQUERADE").CombinedOutput(); err != nil {
		return fmt.Errorf("setup iptables masquerade: %w (%s)", err, string(out))
	}

	// 6. Forwarding rules for bridge
	_ = exec.Command("sudo", "iptables", "-A", "FORWARD", "-i", m.BridgeName, "-j", "ACCEPT").Run()
	_ = exec.Command("sudo", "iptables", "-A", "FORWARD", "-o", m.BridgeName, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT").Run()

	return nil
}

// CreateTAP creates an unprivileged TAP device owned by current UID and attaches it to the bridge
func (m *NetworkManager) CreateTAP(tapName string) error {
	uid := strconv.Itoa(os.Getuid())

	// 1. Delete if stale tap exists
	if interfaceExists(tapName) {
		_ = exec.Command("sudo", "ip", "link", "delete", tapName).Run()
	}

	// 2. Create TAP owned by current user UID
	if out, err := exec.Command("sudo", "ip", "tuntap", "add", "dev", tapName, "mode", "tap", "user", uid).CombinedOutput(); err != nil {
		return fmt.Errorf("create tap %s for uid %s: %w (%s)", tapName, uid, err, string(out))
	}

	// 3. Attach to bridge
	if out, err := exec.Command("sudo", "ip", "link", "set", tapName, "master", m.BridgeName).CombinedOutput(); err != nil {
		return fmt.Errorf("attach tap %s to bridge %s: %w (%s)", tapName, m.BridgeName, err, string(out))
	}

	// 4. Bring TAP UP
	if out, err := exec.Command("sudo", "ip", "link", "set", tapName, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("bring up tap %s: %w (%s)", tapName, err, string(out))
	}

	return nil
}

// SetupPortForward adds a DNAT rule forwarding hostPort to the control-plane API server (port 6443)
func (m *NetworkManager) SetupPortForward(hostPort int, cpIP string) error {
	if hostPort <= 0 {
		return nil
	}
	dest := fmt.Sprintf("%s:6443", cpIP)
	cmd := exec.Command("sudo", "iptables", "-t", "nat", "-A", "PREROUTING", "-p", "tcp", "--dport", strconv.Itoa(hostPort), "-j", "DNAT", "--to-destination", dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("setup port forward to %s: %w (%s)", dest, err, string(out))
	}
	return nil
}

// Teardown cleans up bridge, TAPs, and iptables rules
func (m *NetworkManager) Teardown(tapNames []string) error {
	for _, tap := range tapNames {
		if interfaceExists(tap) {
			_ = exec.Command("sudo", "ip", "link", "delete", tap).Run()
		}
	}

	subnetStr := m.Subnet.String()
	_ = exec.Command("sudo", "iptables", "-t", "nat", "-D", "POSTROUTING", "-s", subnetStr, "!", "-o", m.BridgeName, "-j", "MASQUERADE").Run()
	_ = exec.Command("sudo", "iptables", "-D", "FORWARD", "-i", m.BridgeName, "-j", "ACCEPT").Run()
	_ = exec.Command("sudo", "iptables", "-D", "FORWARD", "-o", m.BridgeName, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT").Run()

	if interfaceExists(m.BridgeName) {
		_ = exec.Command("sudo", "ip", "link", "delete", m.BridgeName).Run()
	}

	return nil
}

func interfaceExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}
