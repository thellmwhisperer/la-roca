package securefile_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

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
