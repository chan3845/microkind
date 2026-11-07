package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// DefaultVsockPort is the guest vsock port where fckind-agent listens.
const DefaultVsockPort = 1024

// MaxFrameSize is the sanity limit for a single framed message (256 MB)
const MaxFrameSize = 256 * 1024 * 1024

// Operation types for vsock RPC
const (
	OpPing            = "ping"
	OpExec            = "exec"
	OpWriteFile       = "write_file"
	OpReadFile        = "read_file"
	OpLoadImage       = "load_image"
	OpConfigureRoutes = "configure_routes"
)

// Request is the generic framing wrapper for RPC requests.
type Request struct {
	Op      string          `json:"op"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Response is the generic framing wrapper for RPC responses.
type Response struct {
	Success bool            `json:"success"`
	Error   string          `json:"error,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// PingResponse payload for OpPing
type PingResponse struct {
	Hostname string `json:"hostname"`
	Uptime   int64  `json:"uptime_sec"`
	Status   string `json:"status"`
}

// ExecRequest payload for OpExec
type ExecRequest struct {
	Command    []string          `json:"command"`
	Env        map[string]string `json:"env,omitempty"`
	Dir        string            `json:"dir,omitempty"`
	Stdin      []byte            `json:"stdin,omitempty"`
	TimeoutSec int               `json:"timeout_sec,omitempty"`
}

// ExecResponse payload for OpExec
type ExecResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   []byte `json:"stdout"`
	Stderr   []byte `json:"stderr"`
}

// WriteFileRequest payload for OpWriteFile
type WriteFileRequest struct {
	Path        string `json:"path"`
	Content     []byte `json:"content"`
	Permissions uint32 `json:"permissions"` // e.g. 0644
	Append      bool   `json:"append,omitempty"`
}

// WriteFileResponse payload for OpWriteFile
type WriteFileResponse struct {
	BytesWritten int `json:"bytes_written"`
}

// ReadFileRequest payload for OpReadFile
type ReadFileRequest struct {
	Path string `json:"path"`
}

// ReadFileResponse payload for OpReadFile
type ReadFileResponse struct {
	Content []byte `json:"content"`
	Size    int64  `json:"size"`
}

// RouteEntry defines a static pod network route
type RouteEntry struct {
	Subnet  string `json:"subnet"`  // e.g. "10.244.1.0/24"
	Gateway string `json:"gateway"` // e.g. "172.30.0.3"
	Dev     string `json:"dev,omitempty"`
}

// ConfigureRoutesRequest payload for OpConfigureRoutes
type ConfigureRoutesRequest struct {
	Routes []RouteEntry `json:"routes"`
}

// ConfigureRoutesResponse payload for OpConfigureRoutes
type ConfigureRoutesResponse struct {
	AppliedCount int `json:"applied_count"`
}

// LoadImageResponse payload for OpLoadImage
type LoadImageResponse struct {
	Message string `json:"message"`
}

// FrameReader reads length-prefixed JSON frames
type FrameReader struct {
	r io.Reader
}

// NewFrameReader creates a new FrameReader
func NewFrameReader(r io.Reader) *FrameReader {
	return &FrameReader{r: r}
}

// ReadFrame reads a 4-byte big-endian length prefixed message
func (fr *FrameReader) ReadFrame() ([]byte, error) {
	var length uint32
	if err := binary.Read(fr.r, binary.BigEndian, &length); err != nil {
		return nil, err
	}
	if length > MaxFrameSize {
		return nil, fmt.Errorf("frame size exceeds maximum limit (%d bytes, max %d)", length, MaxFrameSize)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(fr.r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// FrameWriter writes length-prefixed JSON frames
type FrameWriter struct {
	w io.Writer
}

// NewFrameWriter creates a new FrameWriter
func NewFrameWriter(w io.Writer) *FrameWriter {
	return &FrameWriter{w: w}
}

// Write implements io.Writer by delegating to underlying writer
func (fw *FrameWriter) Write(p []byte) (int, error) {
	return fw.w.Write(p)
}

// WriteFrame writes a 4-byte big-endian length prefixed message
func (fw *FrameWriter) WriteFrame(data []byte) error {
	length := uint32(len(data))
	if err := binary.Write(fw.w, binary.BigEndian, length); err != nil {
		return err
	}
	_, err := fw.w.Write(data)
	return err
}

// SendRequest writes a Request as a framed JSON message
func SendRequest(w io.Writer, op string, payload any) error {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal payload: %w", err)
		}
		raw = b
	}
	req := Request{Op: op, Payload: raw}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}
	if fw, ok := w.(*FrameWriter); ok {
		return fw.WriteFrame(data)
	}
	return NewFrameWriter(w).WriteFrame(data)
}

// SendResponse writes a Response as a framed JSON message
func SendResponse(w io.Writer, success bool, errStr string, payload any) error {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal response payload: %w", err)
		}
		raw = b
	}
	resp := Response{
		Success: success,
		Error:   errStr,
		Payload: raw,
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("failed to marshal response: %w", err)
	}
	if fw, ok := w.(*FrameWriter); ok {
		return fw.WriteFrame(data)
	}
	return NewFrameWriter(w).WriteFrame(data)
}
