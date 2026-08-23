package mcpplug

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
