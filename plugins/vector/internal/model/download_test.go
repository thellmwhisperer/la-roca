package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
)

func TestEnsureDownloadsOnceWithHashAndAtomicRename(t *testing.T) {
	payload := []byte("synthetic-embedding-weights-for-the-local-engine")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	var events []engine.Event
	path, err := Ensure(context.Background(), root, Manifest{
		ID: ID, SHA256: digest, Bytes: int64(len(payload)), URL: server.URL + "/" + FileName,
	}, func(event engine.Event) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("downloads = %d, want 1", hits)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("stored %q", got)
	}
	if _, err := os.Stat(path + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("partial file survived: %v", err)
	}
	if !strings.Contains(path, filepath.Join("models", ID, digest)) {
		t.Fatalf("path %q is not under models/<id>/<sha256>", path)
	}
	again, err := Ensure(context.Background(), root, Manifest{
		ID: ID, SHA256: digest, Bytes: int64(len(payload)), URL: server.URL + "/" + FileName,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again != path || hits != 1 {
		t.Fatalf("second ensure re-downloaded: path %q hits %d", again, hits)
	}
	sawProgress := false
	for _, event := range events {
		if event.Kind == engine.KindProgress && event.Stage == "download" {
			sawProgress = true
		}
		lower := strings.ToLower(event.Line())
		for _, word := range []string{"ollama", "gguf", "llama"} {
			if strings.Contains(lower, word) {
				t.Fatalf("download event leaked %q: %q", word, event.Line())
			}
		}
	}
	if !sawProgress {
		t.Fatal("download emitted no progress events")
	}
}

func TestEnsureRejectsHashMismatchAndLeavesNoPartial(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-the-pinned-bytes"))
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	_, err := Ensure(context.Background(), root, Manifest{
		ID: ID, SHA256: strings.Repeat("ab", 32), Bytes: 19, URL: server.URL,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "embedding model") {
		t.Fatalf("mismatch error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "models", ID))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".partial") {
			t.Fatalf("partial survived a failed download: %s", entry.Name())
		}
	}
}

func TestPathIsRelativeToSelectedDataDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "isolated-home")
	path := FilePath(root, Manifest{ID: "nomic-embed-text-v2-moe", SHA256: SHA256})
	if !strings.HasPrefix(path, root) || strings.Contains(path, string(os.PathSeparator)+".roca"+string(os.PathSeparator)) {
		t.Fatalf("path escaped the selected data directory: %q", path)
	}
}
