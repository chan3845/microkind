package vsock

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"fckind/pkg/protocol"
)

// Client is a host-side client connected to guest fckind-agent over Firecracker virtio-vsock
type Client struct {
	udsPath string
}

// NewClient creates a new host vsock client for a specific VM
func NewClient(udsPath string) *Client {
	return &Client{udsPath: udsPath}
}

// dial connects to Firecracker's vsock Unix domain socket and sends CONNECT 1024
func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.udsPath)
	if err != nil {
		return nil, fmt.Errorf("dial vsock unix socket %s: %w", c.udsPath, err)
	}

	// Handshake with Firecracker vsock multiplexer
	connectMsg := fmt.Sprintf("CONNECT %d\n", protocol.DefaultVsockPort)
	if _, err := conn.Write([]byte(connectMsg)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write vsock CONNECT: %w", err)
	}

	reader := bufio.NewReader(conn)
	resp, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read vsock handshake response: %w", err)
	}

	resp = strings.TrimSpace(resp)
	if !strings.HasPrefix(resp, "OK") {
		conn.Close()
		return nil, fmt.Errorf("vsock handshake rejected: %s", resp)
	}

	return conn, nil
}

// WaitForReady polls the guest agent until Ping succeeds or timeout expires
func (c *Client) WaitForReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		pingCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		_, err := c.Ping(pingCtx)
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %v waiting for fckind-agent on %s", timeout, c.udsPath)
}

// Ping checks if the guest agent is alive and retrieves basic system info
func (c *Client) Ping(ctx context.Context) (*protocol.PingResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := protocol.SendRequest(conn, protocol.OpPing, nil); err != nil {
		return nil, fmt.Errorf("send ping: %w", err)
	}

	fr := protocol.NewFrameReader(conn)
	frame, err := fr.ReadFrame()
	if err != nil {
		return nil, fmt.Errorf("read ping frame: %w", err)
	}

	var resp protocol.Response
	if err := json.Unmarshal(frame, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal ping response: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("ping returned error: %s", resp.Error)
	}

	var pingResp protocol.PingResponse
	if err := json.Unmarshal(resp.Payload, &pingResp); err != nil {
		return nil, fmt.Errorf("unmarshal ping payload: %w", err)
	}

	return &pingResp, nil
}

// Exec runs a command inside the guest VM and returns exit code, stdout, and stderr
func (c *Client) Exec(ctx context.Context, cmd []string, env map[string]string, dir string, stdin []byte) (*protocol.ExecResponse, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	req := protocol.ExecRequest{
		Command: cmd,
		Env:     env,
		Dir:     dir,
		Stdin:   stdin,
	}

	if err := protocol.SendRequest(conn, protocol.OpExec, req); err != nil {
		return nil, fmt.Errorf("send exec: %w", err)
	}

	fr := protocol.NewFrameReader(conn)
	frame, err := fr.ReadFrame()
	if err != nil {
		return nil, fmt.Errorf("read exec frame: %w", err)
	}

	var resp protocol.Response
	if err := json.Unmarshal(frame, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal exec response: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("exec error: %s", resp.Error)
	}

	var execResp protocol.ExecResponse
	if err := json.Unmarshal(resp.Payload, &execResp); err != nil {
		return nil, fmt.Errorf("unmarshal exec payload: %w", err)
	}

	return &execResp, nil
}

// WriteFile writes file content to the specified guest path
func (c *Client) WriteFile(ctx context.Context, path string, content []byte, perm uint32) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	req := protocol.WriteFileRequest{
		Path:        path,
		Content:     content,
		Permissions: perm,
	}

	if err := protocol.SendRequest(conn, protocol.OpWriteFile, req); err != nil {
		return fmt.Errorf("send write_file: %w", err)
	}

	fr := protocol.NewFrameReader(conn)
	frame, err := fr.ReadFrame()
	if err != nil {
		return fmt.Errorf("read write_file frame: %w", err)
	}

	var resp protocol.Response
	if err := json.Unmarshal(frame, &resp); err != nil {
		return fmt.Errorf("unmarshal write_file response: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("write_file error: %s", resp.Error)
	}

	return nil
}

// ReadFile reads the full content of a file from the guest VM
func (c *Client) ReadFile(ctx context.Context, path string) ([]byte, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	req := protocol.ReadFileRequest{Path: path}
	if err := protocol.SendRequest(conn, protocol.OpReadFile, req); err != nil {
		return nil, fmt.Errorf("send read_file: %w", err)
	}

	fr := protocol.NewFrameReader(conn)
	frame, err := fr.ReadFrame()
	if err != nil {
		return nil, fmt.Errorf("read read_file frame: %w", err)
	}

	var resp protocol.Response
	if err := json.Unmarshal(frame, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal read_file response: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("read_file error: %s", resp.Error)
	}

	var readResp protocol.ReadFileResponse
	if err := json.Unmarshal(resp.Payload, &readResp); err != nil {
		return nil, fmt.Errorf("unmarshal read_file payload: %w", err)
	}

	return readResp.Content, nil
}

// LoadImage streams a docker tarball reader into the guest VM's containerd
func (c *Client) LoadImage(ctx context.Context, imageTarReader io.Reader) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	data, err := io.ReadAll(imageTarReader)
	if err != nil {
		return fmt.Errorf("read image stream: %w", err)
	}

	if err := protocol.SendRequest(conn, protocol.OpLoadImage, data); err != nil {
		return fmt.Errorf("send load_image: %w", err)
	}

	fr := protocol.NewFrameReader(conn)
	frame, err := fr.ReadFrame()
	if err != nil {
		return fmt.Errorf("read load_image frame: %w", err)
	}

	var resp protocol.Response
	if err := json.Unmarshal(frame, &resp); err != nil {
		return fmt.Errorf("unmarshal load_image response: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("load_image error: %s", resp.Error)
	}

	return nil
}
