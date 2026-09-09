package cli

import (
	"os"
	"path/filepath"
	"testing"
)

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
