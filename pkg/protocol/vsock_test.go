package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFrameReaderWriter(t *testing.T) {
	buf := new(bytes.Buffer)
	fw := NewFrameWriter(buf)
	fr := NewFrameReader(buf)

	data := []byte("hello virtio-vsock")
	if err := fw.WriteFrame(data); err != nil {
		t.Fatalf("WriteFrame failed: %v", err)
	}

	read, err := fr.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame failed: %v", err)
	}

	if !bytes.Equal(data, read) {
		t.Fatalf("expected %q, got %q", string(data), string(read))
	}
}

func TestSendRequestAndResponse(t *testing.T) {
	buf := new(bytes.Buffer)

	// Send Ping Request
	if err := SendRequest(buf, OpPing, nil); err != nil {
		t.Fatalf("SendRequest failed: %v", err)
	}

	fr := NewFrameReader(buf)
	frame, err := fr.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame failed: %v", err)
	}

	var req Request
	if err := json.Unmarshal(frame, &req); err != nil {
		t.Fatalf("unmarshal Request: %v", err)
	}
	if req.Op != OpPing {
		t.Fatalf("expected op %q, got %q", OpPing, req.Op)
	}

	// Send Exec Response
	respPayload := ExecResponse{
		ExitCode: 0,
		Stdout:   []byte("Kubernetes v1.37.0"),
		Stderr:   nil,
	}
	respBuf := new(bytes.Buffer)
	if err := SendResponse(respBuf, true, "", respPayload); err != nil {
		t.Fatalf("SendResponse failed: %v", err)
	}

	respFr := NewFrameReader(respBuf)
	respFrame, err := respFr.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame response: %v", err)
	}

	var resp Response
	if err := json.Unmarshal(respFrame, &resp); err != nil {
		t.Fatalf("unmarshal Response: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success true, got %v (err: %s)", resp.Success, resp.Error)
	}

	var execResp ExecResponse
	if err := json.Unmarshal(resp.Payload, &execResp); err != nil {
		t.Fatalf("unmarshal ExecResponse: %v", err)
	}
	if string(execResp.Stdout) != "Kubernetes v1.37.0" {
		t.Fatalf("unexpected stdout: %s", string(execResp.Stdout))
	}
}

func TestWriteFileRequestSerialization(t *testing.T) {
	req := WriteFileRequest{
		Path:        "/etc/test.conf",
		Content:     []byte("test=true"),
		Permissions: 0644,
		Append:      true,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var parsed WriteFileRequest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if !parsed.Append {
		t.Fatalf("expected Append true, got %v", parsed.Append)
	}
	if parsed.Path != req.Path {
		t.Fatalf("expected path %s, got %s", req.Path, parsed.Path)
	}
}

func TestMaxFrameSizeExceeded(t *testing.T) {
	buf := new(bytes.Buffer)
	// Write a length prefix exceeding MaxFrameSize
	excessLength := uint32(MaxFrameSize + 1)
	buf.WriteByte(byte(excessLength >> 24))
	buf.WriteByte(byte(excessLength >> 16))
	buf.WriteByte(byte(excessLength >> 8))
	buf.WriteByte(byte(excessLength))

	fr := NewFrameReader(buf)
	_, err := fr.ReadFrame()
	if err == nil {
		t.Fatalf("expected error for frame exceeding MaxFrameSize, got nil")
	}
}
