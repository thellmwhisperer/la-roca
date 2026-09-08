package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRenderSnapshotDoctorReportsCountAndSize(t *testing.T) {
	var out bytes.Buffer
	env := &cliEnv{out: &out, errOut: io.Discard}
	renderSnapshotDoctor(env, leftoverSnapshots{
		Root: "/tmp", Count: 6, Bytes: 9410884608,
	})
	got := out.String()
	for _, want := range []string{
		"read-only snapshots:",
		"6 leftover directories",
		"9410884608 bytes",
		"/tmp",
		"interactive `roca doctor` offers to delete",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("doctor snapshot narration missing %q:\n%s", want, got)
		}
	}
}

func TestOfferSnapshotCleanupDeletesOnlyInspectedOrphansOnYes(t *testing.T) {
	tmp := t.TempDir()
	orphan := filepath.Join(tmp, leftoverSnapshotPrefix+"orphan")
	if err := os.Mkdir(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "payload"), make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	env := &cliEnv{out: &out, errOut: io.Discard}
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("y\n"))
	previous := terminalInput
	terminalInput = func(any) bool { return true }
	t.Cleanup(func() { terminalInput = previous })
	residue := inspectLeftoverSnapshots(tmp)
	newSnapshot := filepath.Join(tmp, leftoverSnapshotPrefix+"new")
	if err := os.Mkdir(newSnapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	newPayload := filepath.Join(newSnapshot, "payload")
	if err := os.WriteFile(newPayload, []byte("active snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.offerSnapshotCleanup(cmd, residue); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan still present: %v", err)
	}
	if payload, err := os.ReadFile(newPayload); err != nil || string(payload) != "active snapshot" {
		t.Fatalf("uninspected snapshot changed: payload=%q, err=%v", payload, err)
	}
	if !strings.Contains(out.String(), "removed 1 leftover read-only snapshot directories · 32 bytes") {
		t.Fatalf("cleanup did not narrate removal:\n%s", out.String())
	}
}
