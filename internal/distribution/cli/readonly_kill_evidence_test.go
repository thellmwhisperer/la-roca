//go:build snapshot_evidence

package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublishedBinaryKillLeavesReadOnlySnapshotOrphans(t *testing.T) {
	published := publishedRocaV1823(t)
	home := t.TempDir()
	tmp := t.TempDir()
	// Only this synthetic home is visible to the published process. Inherited
	// agent roots, configuration and federation paths must never enter the lab.
	env := []string{"HOME=" + home, "TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp,
		"PATH=" + home, "ROCA_MODELS_ORDER=none"}
	t.Cleanup(func() {
		if err := os.RemoveAll(tmp); err != nil {
			t.Errorf("remove evidence snapshots: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	dbPath := filepath.Join(home, ".roca", "roca.db")
	init := exec.CommandContext(ctx, published, "init", "--db-path", dbPath)
	init.Env = env
	init.Dir = home
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("published init: %v\n%s", err, out)
	}
	corpus := filepath.Join(home, ".roca", "plugins", "roca-corpus", "roca-corpus.db")
	if err := appendBallast(corpus, 1<<20); err != nil {
		t.Fatal(err)
	}

	if bytes := leftoverDirSize(home); bytes > 4<<20 {
		t.Fatalf("lab is %d bytes; maximum is 4 MiB", bytes)
	}
	cmd := exec.CommandContext(ctx, published, "--db-path", dbPath, "mcp", "serve")
	cmd.Env = append(env, "ROCA_READ_ONLY=1")
	cmd.Dir = home
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close() })
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdout.Close() })
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	if _, err := io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"snapshot-evidence","version":"1"}}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	var ready struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(stdout).Decode(&ready); err != nil {
		t.Fatalf("published reader initialization: %v", err)
	}
	if ready.ID != 1 || len(ready.Result) == 0 || string(ready.Result) == "null" {
		t.Fatalf("published reader did not initialize: %+v", ready)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill initialized published reader: %v", err)
	}
	_ = cmd.Wait()
	// Count what survived the kill, not a directory observed before cleanup.
	pattern := filepath.Join(tmp, leftoverSnapshotPrefix+"*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	var bytes int64
	for _, dir := range matches {
		bytes += leftoverDirSize(dir)
	}
	t.Logf("published v1.82.3: %d snapshot dirs, %d bytes after kill; cleanup registered", len(matches), bytes)
	if len(matches) == 0 {
		t.Fatal("published binary did not leave a snapshot directory after a mid-flight kill")
	}
}

func publishedRocaV1823(t *testing.T) string {
	t.Helper()
	path := os.Getenv("ROCA_SNAPSHOT_PUBLISHED_BIN")
	if !filepath.IsAbs(path) {
		t.Fatal("set ROCA_SNAPSHOT_PUBLISHED_BIN to an absolute pinned v1.82.3 executable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.Env = []string{"HOME=" + t.TempDir()}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "v1.82.3") {
		t.Fatal("published baseline must execute and report v1.82.3")
	}
	return path
}

func appendBallast(path string, n int) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(make([]byte, n))
	return err
}
