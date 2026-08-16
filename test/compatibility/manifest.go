package compatibility

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const ManifestSchema = "la-roca.data-split-oracle-manifest/v1"

type FileDigest struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Schema  string     `json:"schema"`
	Fixture FileDigest `json:"fixture"`
	Golden  FileDigest `json:"golden"`
}

func VerifyBundle(root string) (Manifest, error) {
	var manifest Manifest
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return manifest, fmt.Errorf("read oracle manifest: %w", err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, fmt.Errorf("decode oracle manifest: %w", err)
	}
	if manifest.Schema != ManifestSchema {
		return manifest, fmt.Errorf("oracle manifest is not a %s bundle", ManifestSchema)
	}
	if err := verifyFileDigest(root, manifest.Fixture); err != nil {
		return manifest, fmt.Errorf("fixture digest: %w", err)
	}
	if err := verifyFileDigest(root, manifest.Golden); err != nil {
		return manifest, fmt.Errorf("golden digest: %w", err)
	}
	return manifest, nil
}

func verifyFileDigest(root string, file FileDigest) error {
	if file.Path == "" || filepath.Base(file.Path) != file.Path {
		return fmt.Errorf("unsafe bundle path %q", file.Path)
	}
	raw, err := os.ReadFile(filepath.Join(root, file.Path))
	if err != nil {
		return fmt.Errorf("read %s: %w", file.Path, err)
	}
	digest := sha256.Sum256(raw)
	got := hex.EncodeToString(digest[:])
	if subtle.ConstantTimeCompare([]byte(got), []byte(file.SHA256)) != 1 {
		return fmt.Errorf("%s digest = %s, want %s", file.Path, got, file.SHA256)
	}
	return nil
}
