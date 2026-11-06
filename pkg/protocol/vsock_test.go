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
