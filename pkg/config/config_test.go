package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultClusterConfig(t *testing.T) {
	cfg := DefaultClusterConfig("test-cluster")
	if cfg.Name != "test-cluster" {
		t.Fatalf("expected cluster name test-cluster, got %s", cfg.Name)
	}
	if len(cfg.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(cfg.Nodes))
	}
	if cfg.K0s.Storage.Type != "kine" {
		t.Fatalf("expected adaptive single-CP storage to be kine, got %s", cfg.K0s.Storage.Type)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate failed: %v", err)
	}
}

func TestAdaptiveEtcdStorage(t *testing.T) {
	yamlContent := `
name: ha-cluster
nodes:
  - role: control-plane
  - role: control-plane
  - role: worker
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "cluster.yaml")
	if err := os.WriteFile(path, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("write temp yaml: %v", err)
	}

	cfg, err := LoadConfig(path, "")
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}

	if cfg.K0s.Storage.Type != "etcd" {
		t.Fatalf("expected multi-CP adaptive storage to be etcd, got %s", cfg.K0s.Storage.Type)
	}
}
