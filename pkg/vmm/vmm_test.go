package vmm

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
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

func TestSetVMState(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer ln.Close()

	var gotMethod, gotPath, gotState string
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotState = body["state"]
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	go srv.Serve(ln)
	defer srv.Close()

	if err := SetVMState(context.Background(), sockPath, "Paused"); err != nil {
		t.Fatalf("SetVMState(Paused) failed: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/vm" || gotState != "Paused" {
		t.Fatalf("unexpected request: method=%s path=%s state=%s", gotMethod, gotPath, gotState)
	}

	if err := SetVMState(context.Background(), sockPath, "Resumed"); err != nil {
		t.Fatalf("SetVMState(Resumed) failed: %v", err)
	}
	if gotState != "Resumed" {
		t.Fatalf("expected Resumed, got %s", gotState)
	}
}
