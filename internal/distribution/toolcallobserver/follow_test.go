package toolcallobserver

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

func TestFollowWritesAShellCommandAndItsOutputWithinSeconds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, claudeSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","timestamp":"2026-08-01T10:00:00Z","message":{"content":"start"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, Session{Harness: "claude", Kind: parsers.KindClaudeSession, Path: path}, &out, FollowOptions{
			PollEvery: 20 * time.Millisecond,
		})
	}()
	time.Sleep(60 * time.Millisecond)
	appendFile(t, path, `{"type":"assistant","timestamp":"2026-08-01T10:00:01Z","message":{"content":[{"type":"tool_use","id":"live-1","name":"Bash","input":{"command":"echo live-lab"}}]}}`+"\n")
	appendFile(t, path, `{"type":"user","timestamp":"2026-08-01T10:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"live-1","content":"live-lab\n"}]}}`+"\n")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := out.String()
		if strings.Contains(got, "echo live-lab") && strings.Contains(got, "live-lab") {
			cancel()
			select {
			case err := <-done:
				if err != nil && err != context.Canceled {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("follow did not exit")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	t.Fatalf("observer output missing the live shell call:\n%s", out.String())
}

func appendFile(t *testing.T, path, line string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(line); err != nil {
		t.Fatal(err)
	}
}
