package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"fckind/pkg/protocol"
	"golang.org/x/sys/unix"
)

// RunAgent starts the vsock RPC daemon on AF_VSOCK port 1024
func RunAgent() error {
	log.Printf("[fckind-agent] Starting guest vsock daemon on port %d...", protocol.DefaultVsockPort)

	// Create vsock stream socket
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("failed to create AF_VSOCK socket: %w", err)
	}
	defer unix.Close(fd)

	// Bind to ANY CID on port 1024
	sa := &unix.SockaddrVM{
		CID:  unix.VMADDR_CID_ANY,
		Port: protocol.DefaultVsockPort,
	}

	if err := unix.Bind(fd, sa); err != nil {
		return fmt.Errorf("failed to bind vsock socket to port %d: %w", protocol.DefaultVsockPort, err)
	}

	if err := unix.Listen(fd, 32); err != nil {
		return fmt.Errorf("failed to listen on vsock socket: %w", err)
	}

	log.Printf("[fckind-agent] Listening on AF_VSOCK port %d. Ready for host commands.", protocol.DefaultVsockPort)

	for {
		nfd, from, err := unix.Accept(fd)
		if err != nil {
			log.Printf("[fckind-agent] Accept error: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		go handleVsockConn(nfd, from)
	}
}

func handleVsockConn(nfd int, from unix.Sockaddr) {
	// Security: Only allow connections originating from the host (CID 2).
	// Reject connections from other microVM CIDs or internal loopback containers.
	if sa, ok := from.(*unix.SockaddrVM); ok {
		if sa.CID != unix.VMADDR_CID_HOST {
			log.Printf("[fckind-agent] Blocked unauthorized vsock connection from CID %d (expected host CID %d)", sa.CID, unix.VMADDR_CID_HOST)
			_ = unix.Close(nfd)
			return
		}
	}

	connFile := os.NewFile(uintptr(nfd), "vsock-client")
	defer connFile.Close()

	reader := protocol.NewFrameReader(connFile)
	writer := protocol.NewFrameWriter(connFile)

	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			if err != io.EOF {
				log.Printf("[fckind-agent] Connection read error: %v", err)
			}
			return
		}

		var req protocol.Request
		if err := json.Unmarshal(frame, &req); err != nil {
			_ = protocol.SendResponse(writer, false, fmt.Sprintf("invalid request JSON: %v", err), nil)
			continue
		}

		handleRequest(writer, req)
	}
}

func handleRequest(w io.Writer, req protocol.Request) {
	switch req.Op {
	case protocol.OpPing:
		handlePing(w)
	case protocol.OpExec:
		handleExec(w, req.Payload)
	case protocol.OpWriteFile:
		handleWriteFile(w, req.Payload)
	case protocol.OpReadFile:
		handleReadFile(w, req.Payload)
	case protocol.OpConfigureRoutes:
		handleConfigureRoutes(w, req.Payload)
	case protocol.OpLoadImage:
		handleLoadImage(w, req.Payload)
	default:
		_ = protocol.SendResponse(w, false, fmt.Sprintf("unknown operation: %s", req.Op), nil)
	}
}

func handlePing(w io.Writer) {
	hostname, _ := os.Hostname()
	var info unix.Sysinfo_t
	var uptime int64
	if err := unix.Sysinfo(&info); err == nil {
		uptime = info.Uptime
	}

	resp := protocol.PingResponse{
		Hostname: hostname,
		Uptime:   uptime,
		Status:   "ready",
	}
	_ = protocol.SendResponse(w, true, "", resp)
}

func handleExec(w io.Writer, payload []byte) {
	var req protocol.ExecRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		_ = protocol.SendResponse(w, false, fmt.Sprintf("invalid exec payload: %v", err), nil)
		return
	}

	if len(req.Command) == 0 {
		_ = protocol.SendResponse(w, false, "empty command", nil)
		return
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if req.TimeoutSec > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSec)*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, req.Command[0], req.Command[1:]...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}

	// Environment
	cmd.Env = os.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if len(req.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	resp := protocol.ExecResponse{
		ExitCode: exitCode,
		Stdout:   stdoutBuf.Bytes(),
		Stderr:   stderrBuf.Bytes(),
	}
	_ = protocol.SendResponse(w, true, "", resp)
}

func handleWriteFile(w io.Writer, payload []byte) {
	var req protocol.WriteFileRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		_ = protocol.SendResponse(w, false, fmt.Sprintf("invalid write_file payload: %v", err), nil)
		return
	}

	if err := os.MkdirAll(filepath.Dir(req.Path), 0755); err != nil {
		_ = protocol.SendResponse(w, false, fmt.Sprintf("mkdir failed: %v", err), nil)
		return
	}

	perm := os.FileMode(req.Permissions)
	if perm == 0 {
		perm = 0644
	}

	flags := os.O_CREATE | os.O_WRONLY
	if req.Append {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}

	f, err := os.OpenFile(req.Path, flags, perm)
	if err != nil {
		_ = protocol.SendResponse(w, false, fmt.Sprintf("open file %s failed: %v", req.Path, err), nil)
		return
	}
	defer f.Close()

	n, err := f.Write(req.Content)
	if err != nil {
		_ = protocol.SendResponse(w, false, fmt.Sprintf("write to %s failed: %v", req.Path, err), nil)
		return
	}

	_ = protocol.SendResponse(w, true, "", protocol.WriteFileResponse{BytesWritten: n})
}

func handleReadFile(w io.Writer, payload []byte) {
	var req protocol.ReadFileRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		_ = protocol.SendResponse(w, false, fmt.Sprintf("invalid read_file payload: %v", err), nil)
		return
	}

	data, err := os.ReadFile(req.Path)
	if err != nil {
		_ = protocol.SendResponse(w, false, fmt.Sprintf("read file %s failed: %v", req.Path, err), nil)
		return
	}

	_ = protocol.SendResponse(w, true, "", protocol.ReadFileResponse{
		Content: data,
		Size:    int64(len(data)),
	})
}

var routeLock sync.Mutex

func handleConfigureRoutes(w io.Writer, payload []byte) {
	routeLock.Lock()
	defer routeLock.Unlock()

	var req protocol.ConfigureRoutesRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		_ = protocol.SendResponse(w, false, fmt.Sprintf("invalid configure_routes payload: %v", err), nil)
		return
	}

	applied := 0
	for _, r := range req.Routes {
		dev := r.Dev
		if dev == "" {
			dev = "eth0"
		}
		// Execute: ip route replace <subnet> via <gateway> dev <dev>
		cmd := exec.Command("ip", "route", "replace", r.Subnet, "via", r.Gateway, "dev", dev)
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = protocol.SendResponse(w, false, fmt.Sprintf("failed to add route %s via %s: %v (%s)", r.Subnet, r.Gateway, err, string(out)), nil)
			return
		}
		applied++
	}

	_ = protocol.SendResponse(w, true, "", protocol.ConfigureRoutesResponse{AppliedCount: applied})
}

func handleLoadImage(w io.Writer, payload []byte) {
	// Import image tar stream directly into containerd via k0s
	cmd := exec.Command("k0s", "ctr", "images", "import", "-")
	cmd.Stdin = bytes.NewReader(payload)

	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = protocol.SendResponse(w, false, fmt.Sprintf("k0s ctr images import failed: %v (%s)", err, string(out)), nil)
		return
	}

	_ = protocol.SendResponse(w, true, "", protocol.LoadImageResponse{Message: string(out)})
}
