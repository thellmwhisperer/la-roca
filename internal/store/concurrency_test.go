package store_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

const (
	envWriter  = "ROCA_TEST_ESCRITOR"
	envDB      = "ROCA_TEST_BASE"
	envBarrier = "ROCA_TEST_BARRERA"

	writers       = 8
	rowsPerWriter = 5
)

// TestEightProcessesWriteWithoutLosingTransactions is wave 1's gate. These are
// real processes over a barrier, not goroutines: an in-process pool shares the
// handle and proves nothing about contention between processes, which is where
// the lab measured 48 lost transactions.
func TestEightProcessesWriteWithoutLosingTransactions(t *testing.T) {
	if os.Getenv(envWriter) != "" {
		t.Skip("this process is a child writer")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "roca.db")
	barrier := filepath.Join(dir, "barrera")

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.ApplySchema(context.Background(), db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	db.Close()

	var wait sync.WaitGroup
	failures := make(chan string, writers)
	for i := range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			cmd := exec.Command(os.Args[0], "-test.run=TestChildWriter", "-test.v")
			cmd.Env = append(os.Environ(),
				envWriter+"=1", envDB+"="+path, envBarrier+"="+barrier)
			if out, err := cmd.CombinedOutput(); err != nil {
				failures <- string(out)
			}
			_ = i
		}()
	}

	// The barrier releases all eight at once: without it each process starts
	// when the previous one has already finished and the contention never comes
	// into existence.
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile(barrier, []byte("ya"), 0o600); err != nil {
		t.Fatalf("create the barrier: %v", err)
	}
	wait.Wait()
	close(failures)
	for out := range failures {
		t.Errorf("a writer failed:\n%s", out)
	}

	db, err = store.Open(path)
	if err != nil {
		t.Fatalf("reabrir: %v", err)
	}
	defer db.Close()
	if got := countMemories(t, db.SQL()); got != writers*rowsPerWriter {
		t.Errorf("rows = %d, want %d: transactions have been lost",
			got, writers*rowsPerWriter)
	}
}

// TestChildWriter is the body of each writer process. It only does anything
// when the parent invokes it with the barrier's environment.
func TestChildWriter(t *testing.T) {
	if os.Getenv(envWriter) == "" {
		t.Skip("I am not a child writer")
	}

	db, err := store.Open(os.Getenv(envDB))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	waitForBarrier(t, os.Getenv(envBarrier))

	ctx := context.Background()
	for i := range rowsPerWriter {
		err := db.Write(ctx, func(tx *sql.Tx) error {
			// Reading before writing is what breaks with a plain BEGIN.
			var n int
			if err := tx.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n); err != nil {
				return err
			}
			_, err := tx.Exec(
				"INSERT INTO memories (layer, content, origin) VALUES (?, ?, ?)",
				"project", strings.Repeat("x", 16), "agent")
			return err
		})
		if err != nil {
			t.Fatalf("escritura %d: %v", i, err)
		}
	}
}

func waitForBarrier(t *testing.T, path string) {
	t.Helper()
	limit := time.Now().Add(10 * time.Second)
	for time.Now().Before(limit) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the barrier %q never appeared", path)
}
