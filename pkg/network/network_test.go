package network

import (
	"strings"
	"testing"
)

func TestTAPNameLengthAndCollision(t *testing.T) {
	// Previously, test1 and ci collided on hash[0]
	mgr1, err := NewNetworkManager("test1")
	if err != nil {
		t.Fatalf("NewNetworkManager(test1): %v", err)
	}
	mgr2, err := NewNetworkManager("ci")
	if err != nil {
		t.Fatalf("NewNetworkManager(ci): %v", err)
	}

	tap1 := mgr1.TAPName(0, "control-plane")
	tap2 := mgr2.TAPName(0, "control-plane")

	// 1. Length must be <= 15 chars (Linux IFNAMSIZ - 1)
	if len(tap1) > 15 {
		t.Fatalf("TAP name %q exceeds 15 chars (len %d)", tap1, len(tap1))
	}
	if len(tap2) > 15 {
		t.Fatalf("TAP name %q exceeds 15 chars (len %d)", tap2, len(tap2))
	}

	// 2. Must not collide
	if tap1 == tap2 {
		t.Fatalf("collision detected! test1 and ci produced identical TAP name %q", tap1)
	}

	// 3. Worker prefix test
	tapWorker := mgr1.TAPName(1, "worker")
	if !strings.Contains(tapWorker, "wk") {
		t.Fatalf("expected worker prefix in TAP name %q", tapWorker)
	}
	if len(tapWorker) > 15 {
		t.Fatalf("worker TAP name %q exceeds 15 chars (len %d)", tapWorker, len(tapWorker))
	}
}

func TestBridgeNameLength(t *testing.T) {
	mgr, err := NewNetworkManager("a-very-long-cluster-name-that-might-overflow-linux-ifnamsiz")
	if err != nil {
		t.Fatalf("NewNetworkManager error: %v", err)
	}

	if len(mgr.BridgeName) > 15 {
		t.Fatalf("bridge name %q exceeds 15 chars (len %d)", mgr.BridgeName, len(mgr.BridgeName))
	}
}

func TestNodeIPGeneration(t *testing.T) {
	mgr, err := NewNetworkManager("test-cluster")
	if err != nil {
		t.Fatalf("NewNetworkManager error: %v", err)
	}

	ip0 := mgr.NodeIP(0)
	ip1 := mgr.NodeIP(1)

	if !strings.HasSuffix(ip0, ".2") {
		t.Fatalf("expected node 0 to end in .2, got %s", ip0)
	}
	if !strings.HasSuffix(ip1, ".3") {
		t.Fatalf("expected node 1 to end in .3, got %s", ip1)
	}
}

func TestFindAvailableSubnet(t *testing.T) {
	octet, err := findAvailableSubnet(27)
	if err != nil {
		t.Fatalf("findAvailableSubnet error: %v", err)
	}
	if octet < 2 || octet > 249 {
		t.Fatalf("allocated octet out of range: %d", octet)
	}
}
