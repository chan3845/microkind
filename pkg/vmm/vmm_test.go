package vmm

import (
	"path/filepath"
	"testing"
)

func TestNewInstancePaths(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := InstanceConfig{
		ID:        "test-node",
		SocketDir: tmpDir,
	}

	inst, err := NewInstance(cfg)
	if err != nil {
		t.Fatalf("NewInstance error: %v", err)
	}

	expectedAPISock := filepath.Join(tmpDir, "api.sock")
	if inst.APISock != expectedAPISock {
		t.Fatalf("expected APISock %s, got %s", expectedAPISock, inst.APISock)
	}

	expectedVsock := filepath.Join(tmpDir, "vsock.sock")
	if inst.Config.VsockPath != expectedVsock {
		t.Fatalf("expected VsockPath %s, got %s", expectedVsock, inst.Config.VsockPath)
	}

	if inst.PID() != 0 {
		t.Fatalf("expected PID to be 0 prior to start, got %d", inst.PID())
	}
}
