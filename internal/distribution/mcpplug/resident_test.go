package mcpplug

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestResidentChildDiesWhenStdinCloses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("session residency is proven on unix stdio")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "roca-vector")
	marker := filepath.Join(dir, "dead")
	body := "#!/bin/sh\n" +
		"trap 'echo gone > " + marker + "' EXIT\n" +
		"echo '{\"kind\":\"progress\",\"stage\":\"prewarm\",\"message\":\"semantic search: preparing\"}'\n" +
		"echo '{\"kind\":\"result\",\"stage\":\"prewarm\",\"message\":\"semantic search: ready\",\"extra\":{\"prewarm_ms\":12}}'\n" +
		"while IFS= read -r line; do :; done\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(script)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("resident child outlived stdin close")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("child did not run its exit trap: %v", err)
	}
}

func TestResidentCloseTerminatesChildDuringPrewarm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "roca-vector")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCA_VECTOR_RESIDENT_BINARY", script)
	svc, err := service.Open(service.Options{DBPath: filepath.Join(dir, "roca.db"), VectorEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	resident, err := startResidentVector(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- resident.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		_ = resident.cmd.Process.Kill()
		t.Fatal("resident close waited for prewarm")
	}
}

func TestResidentRoutesByIDAndStreamsProductStatus(t *testing.T) {
	status := new(bytes.Buffer)
	resident := &residentVector{status: status, ready: make(chan struct{}), failed: make(chan struct{}),
		pending: map[int64]chan residentEnvelope{2: make(chan residentEnvelope, 1)}}
	input := strings.NewReader("" +
		`{"kind":"progress","stage":"prewarm","message":"semantic search: preparing"}` + "\n" +
		`{"kind":"result","stage":"prewarm","message":"semantic search: ready"}` + "\n" +
		`{"kind":"result","stage":"query","id":1,"result":{"stale":true}}` + "\n" +
		`{"kind":"result","stage":"query","id":2,"result":{"fresh":true}}` + "\n")
	resident.decode(input)
	response := <-resident.pending[2]
	if response.ID != 2 || !bytes.Contains(response.Result, []byte(`"fresh":true`)) {
		t.Fatalf("routed response = %+v", response)
	}
	if status.String() != "semantic search: preparing\n" {
		t.Fatalf("status = %q", status.String())
	}
}

func TestResidentCleanEOFBeforeReadyWakesWaiters(t *testing.T) {
	resident := &residentVector{ready: make(chan struct{}), failed: make(chan struct{}),
		pending: make(map[int64]chan residentEnvelope)}
	resident.decode(strings.NewReader(""))
	err := resident.waitReady(context.Background())
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("waitReady error = %v, want unexpected EOF", err)
	}
}

func TestResidentRequiresVectorConsent(t *testing.T) {
	t.Setenv("ROCA_VECTOR_RESIDENT_BINARY", filepath.Join(t.TempDir(), "missing-vector"))
	disabled, err := service.Open(service.Options{DBPath: filepath.Join(t.TempDir(), "roca.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { disabled.Close() })
	resident, err := consentedResident(context.Background(), disabled)
	if err != nil || resident != nil {
		t.Fatalf("disabled consent started resident: resident=%v err=%v", resident, err)
	}
	enabled, err := service.Open(service.Options{DBPath: filepath.Join(t.TempDir(), "roca.db"), VectorEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { enabled.Close() })
	if _, err := consentedResident(context.Background(), enabled); err == nil {
		t.Fatal("enabled consent did not reach the configured resident boundary")
	}
}

func TestResidentProtocolEnvelope(t *testing.T) {
	raw := []byte(`{"kind":"result","stage":"prewarm","message":"semantic search: ready","extra":{"prewarm_ms":12}}`)
	var envelope residentEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Kind != "result" || envelope.Stage != "prewarm" || envelope.Message == "" {
		t.Fatalf("%+v", envelope)
	}
	if strings.Contains(strings.ToLower(envelope.Message), "ollama") {
		t.Fatalf("product message leaked a runtime name: %q", envelope.Message)
	}
}
