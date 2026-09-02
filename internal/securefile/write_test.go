package securefile_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

func TestReplaceRegularPreservesConcurrentPermissionChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission changes")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	previous := []byte("operator configuration")
	if err := os.WriteFile(path, previous, 0o644); err != nil {
		t.Fatal(err)
	}
	original, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	err = securefile.ReplaceRegular(path, []byte("replacement"), previous, original)
	if err == nil || !strings.Contains(err.Error(), "changed while it was being edited") {
		t.Fatalf("ReplaceRegular error = %v, want concurrent-change refusal", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(previous) {
		t.Fatalf("content = %q, want %q", content, previous)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}

func TestConcurrentBackupsNeverOverwriteEachOther(t *testing.T) {
	const writers = 24
	path := filepath.Join(t.TempDir(), "artifact.md")
	start := make(chan struct{})
	results := make(chan string, writers)
	errors := make(chan error, writers)
	var group sync.WaitGroup
	for index := range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			backup, err := securefile.BackUp(path, []byte(fmt.Sprintf("writer-%d", index)))
			if err != nil {
				errors <- err
				return
			}
			results <- backup
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Error(err)
	}

	seen := make(map[string]bool, writers)
	for backup := range results {
		if seen[backup] {
			t.Errorf("backup path reused: %s", backup)
		}
		seen[backup] = true
		if _, err := os.ReadFile(backup); err != nil {
			t.Errorf("read backup %s: %v", backup, err)
		}
	}
	if len(seen) != writers {
		t.Fatalf("created %d backups, want %d", len(seen), writers)
	}
}
