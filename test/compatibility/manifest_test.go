package compatibility

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyBundleAcceptsItsDigestsAndRejectsChangedGolden(t *testing.T) {
	root := t.TempDir()
	fixture := []byte("synthetic fixture\n")
	golden := []byte("synthetic golden\n")
	if err := os.WriteFile(filepath.Join(root, "fixture.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "golden.json"), golden, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Schema:  ManifestSchema,
		Fixture: digestFor("fixture.json", fixture),
		Golden:  digestFor("golden.json", golden),
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := VerifyBundle(root); err != nil {
		t.Fatalf("verify original bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "golden.json"), append(golden, 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBundle(root); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("changed golden verification error = %v, want digest refusal", err)
	}
}

func digestFor(path string, content []byte) FileDigest {
	digest := sha256.Sum256(content)
	return FileDigest{Path: path, SHA256: hex.EncodeToString(digest[:])}
}
