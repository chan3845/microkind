package cluster

import (
	"testing"

	"fckind/pkg/config"
)

func TestSortedNodeList(t *testing.T) {
	nodes := map[string]NodeState{
		"lab-worker-2":      {Name: "lab-worker-2", Role: config.RoleWorker},
		"lab-worker-1":      {Name: "lab-worker-1", Role: config.RoleWorker},
		"lab-control-plane": {Name: "lab-control-plane", Role: config.RoleControlPlane},
	}

	sorted := sortedNodeList(nodes)
	if len(sorted) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(sorted))
	}
	if sorted[0].Name != "lab-control-plane" || sorted[1].Name != "lab-worker-1" || sorted[2].Name != "lab-worker-2" {
		t.Fatalf("unexpected order: %v, %v, %v", sorted[0].Name, sorted[1].Name, sorted[2].Name)
	}
}

func TestResolveNodeResourcesFallback(t *testing.T) {
	state := &ClusterState{
		Config: config.ClusterConfig{
			Name: "microkind",
			Nodes: []config.NodeConfig{
				{Role: config.RoleControlPlane, VCPUs: 2, MemoryMiB: 2048},
				{Role: config.RoleWorker, VCPUs: 2, MemoryMiB: 4096},
			},
		},
	}

	// Fallback when NodeState does not yet store VCPUs/MemoryMiB
	vcpus, mem := resolveNodeResources(state, NodeState{Name: "microkind-worker-1", Role: config.RoleWorker})
	if vcpus != 2 || mem != 4096 {
		t.Fatalf("expected 2 vCPUs / 4096 MiB from config fallback, got %d / %d", vcpus, mem)
	}

	// Direct values take precedence when stored on NodeState
	vcpus, mem = resolveNodeResources(state, NodeState{Name: "microkind-control-plane", Role: config.RoleControlPlane, VCPUs: 4, MemoryMiB: 3072})
	if vcpus != 4 || mem != 3072 {
		t.Fatalf("expected 4 vCPUs / 3072 MiB from NodeState, got %d / %d", vcpus, mem)
	}
}
