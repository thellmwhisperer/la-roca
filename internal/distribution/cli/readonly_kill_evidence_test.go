package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublishedBinaryKillLeavesReadOnlySnapshotOrphans(t *testing.T) {
	published := publishedRocaV1823(t)
	if published == "" {
		t.Skip("published roca v1.82.3 is not on PATH")
	}
	home := t.TempDir()
	tmp := t.TempDir()
	isolateRuntimeDirs(t, home)
	t.Setenv("TMPDIR", tmp)
	t.Setenv("ROCA_READ_ONLY", "")
	dbPath := filepath.Join(home, ".roca", "roca.db")
	init := exec.Command(published, "init", "--db-path", dbPath)
	init.Env = os.Environ()
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("published init: %v\n%s", err, out)
	}
	corpus := filepath.Join(home, ".roca", "plugins", "roca-corpus", "roca-corpus.db")
	if err := appendBallast(corpus, 48<<20); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ROCA_READ_ONLY", "1")
	cmd := exec.Command(published, "--db-path", dbPath, "_database-scope", "--databases", "corpus")
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pattern := filepath.Join(tmp, leftoverSnapshotPrefix+"*")
	deadline := time.Now().Add(2 * time.Second)
	var matches []string
	for time.Now().Before(deadline) {
		found, err := filepath.Glob(pattern)
		if err != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			t.Fatal(err)
		}
		if len(found) > 0 {
			matches = found
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if len(matches) == 0 {
		matches, _ = filepath.Glob(pattern)
	}
	var bytes int64
	for _, dir := range matches {
		bytes += leftoverDirSize(dir)
	}
	t.Logf("issue #341 published evidence: %s left %d snapshot dirs totalling %d bytes after kill",
		published, len(matches), bytes)
	if len(matches) == 0 {
		t.Fatal("published binary did not leave a snapshot directory after a mid-flight kill")
	}
}

func TestDoctorDoesNotDeleteLeftoversOnItsOwn(t *testing.T) {
	tmp := t.TempDir()
	orphan := filepath.Join(tmp, leftoverSnapshotPrefix+"kept")
	if err := os.Mkdir(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "payload"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	residue := inspectLeftoverSnapshots(tmp)
	if residue.Count != 1 {
		t.Fatalf("inspect count=%d want 1", residue.Count)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("inspect removed leftover: %v", err)
	}
}

func publishedRocaV1823(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("roca")
	if err != nil {
		return ""
	}
	out, err := exec.Command(path, "version").CombinedOutput()
	if err != nil {
		return ""
	}
	if !strings.Contains(string(out), "v1.82.3") {
		return ""
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
