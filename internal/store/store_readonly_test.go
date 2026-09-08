package store

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

const readOnlyHelperEnv = "ROCA_READONLY_TEST_HELPER"

func TestOpenReadOnlyUsesTheLiveFileWithoutCopying(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	dbPath := filepath.Join(tmp, "roca.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, value TEXT);
			INSERT INTO items (value) VALUES ('alpha')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })

	if dirs := leftoverSnapshotDirs(t, tmp); len(dirs) != 0 {
		t.Fatalf("read-only open copied into %v", dirs)
	}
	var value string
	if err := reader.SQL().QueryRow(`SELECT value FROM items`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "alpha" {
		t.Fatalf("value = %q, want alpha", value)
	}
}

func TestOpenReadOnlyReadsCommittedWAL(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	path := filepath.Join(tmp, "live.db")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	if _, err := writer.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
		CREATE TABLE rows (id INTEGER PRIMARY KEY, value TEXT);
		INSERT INTO rows VALUES (1, 'first')`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`INSERT INTO rows VALUES (2, 'second')`); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	var count int
	if err := reader.SQL().QueryRow(`SELECT COUNT(*) FROM rows`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("rows = %d, want 2", count)
	}
	if dirs := leftoverSnapshotDirs(t, tmp); len(dirs) != 0 {
		t.Fatalf("live WAL open copied into %v", dirs)
	}
}

func TestOpenReadOnlyKillLeavesNoDirectory(t *testing.T) {
	if os.Getenv(readOnlyHelperEnv) == "hold" {
		runReadOnlyHoldHelper()
		return
	}
	signals := []struct {
		name string
		fire func(*os.Process) error
	}{
		{name: "kill", fire: (*os.Process).Kill},
		{name: "interrupt", fire: func(p *os.Process) error { return p.Signal(os.Interrupt) }},
	}
	if runtime.GOOS != "windows" {
		signals = append(signals, struct {
			name string
			fire func(*os.Process) error
		}{name: "term", fire: func(p *os.Process) error { return p.Signal(syscall.SIGTERM) }})
	}
	for _, tc := range signals {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			dbPath := filepath.Join(tmp, "roca.db")
			db, err := Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestOpenReadOnlyKillLeavesNoDirectory$")
			cmd.Env = append(os.Environ(),
				readOnlyHelperEnv+"=hold",
				"ROCA_READONLY_DB="+dbPath,
				"TMPDIR="+tmp,
			)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if err := waitHelperReady(stdout, 5*time.Second); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatalf("helper: %v\n%s", err, stderr.String())
			}
			if dirs := leftoverSnapshotDirs(t, tmp); len(dirs) != 0 {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatalf("helper copied into %v", dirs)
			}
			if err := tc.fire(cmd.Process); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal(err)
			}
			_, _ = cmd.Process.Wait()
			dirs := leftoverSnapshotDirs(t, tmp)
			t.Logf("issue #341 branch evidence: after %s, leftover snapshot dirs=%d", tc.name, len(dirs))
			if len(dirs) != 0 {
				t.Fatalf("%s left snapshot directories %v", tc.name, dirs)
			}
		})
	}
}

func runReadOnlyHoldHelper() {
	reader, err := OpenReadOnly(os.Getenv("ROCA_READONLY_DB"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper open: %v\n", err)
		os.Exit(1)
	}
	defer reader.Close()
	fmt.Println("ready")
	os.Stdout.Sync()
	select {}
}

func waitHelperReady(stdout io.Reader, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			done <- err
			return
		}
		if strings.TrimSpace(line) != "ready" {
			done <- fmt.Errorf("helper said %q", line)
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return errors.New("helper did not become ready")
	}
}

func leftoverSnapshotDirs(t *testing.T, root string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, "roca-read-only-snapshot-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}
