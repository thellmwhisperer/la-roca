package compatibility

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyBundleAcceptsItsSealAndRejectsChangedGolden(t *testing.T) {
	root := t.TempDir()
	fixture := []byte("synthetic fixture\n")
	golden := []byte("synthetic golden\n")
	if err := os.WriteFile(filepath.Join(root, "fixture.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "golden.json"), golden, 0o600); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Schema:  ManifestSchema,
		Sealed:  true,
		Fixture: digestFor("fixture.json", fixture),
		Golden:  digestFor("golden.json", golden),
	}
	manifest.Signature = Signature{
		Algorithm: "ed25519",
		PublicKey: base64.StdEncoding.EncodeToString(public),
		Value:     base64.StdEncoding.EncodeToString(ed25519.Sign(private, manifest.signingPayload())),
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := VerifyBundle(root, public); err != nil {
		t.Fatalf("verify original bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "golden.json"), append(golden, 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBundle(root, public); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("changed golden verification error = %v, want digest refusal", err)
	}
}

func digestFor(path string, content []byte) FileDigest {
	digest := sha256.Sum256(content)
	return FileDigest{Path: path, SHA256: hex.EncodeToString(digest[:])}
}
