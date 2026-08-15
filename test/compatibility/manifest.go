package compatibility

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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

type Signature struct {
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
	Value     string `json:"value"`
}

type Manifest struct {
	Schema    string     `json:"schema"`
	Sealed    bool       `json:"sealed"`
	Fixture   FileDigest `json:"fixture"`
	Golden    FileDigest `json:"golden"`
	Signature Signature  `json:"signature"`
}

func VerifyBundle(root string, pinnedPublicKey ed25519.PublicKey) (Manifest, error) {
	var manifest Manifest
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return manifest, fmt.Errorf("read oracle manifest: %w", err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, fmt.Errorf("decode oracle manifest: %w", err)
	}
	if manifest.Schema != ManifestSchema || !manifest.Sealed {
		return manifest, fmt.Errorf("oracle manifest is not a sealed %s bundle", ManifestSchema)
	}
	if err := verifyFileDigest(root, manifest.Fixture); err != nil {
		return manifest, fmt.Errorf("fixture digest: %w", err)
	}
	if err := verifyFileDigest(root, manifest.Golden); err != nil {
		return manifest, fmt.Errorf("golden digest: %w", err)
	}
	if manifest.Signature.Algorithm != "ed25519" {
		return manifest, fmt.Errorf("oracle signature algorithm = %q, want ed25519", manifest.Signature.Algorithm)
	}
	declared, err := base64.StdEncoding.DecodeString(manifest.Signature.PublicKey)
	if err != nil {
		return manifest, fmt.Errorf("decode oracle public key: %w", err)
	}
	if len(declared) != ed25519.PublicKeySize || len(pinnedPublicKey) != ed25519.PublicKeySize ||
		subtle.ConstantTimeCompare(declared, pinnedPublicKey) != 1 {
		return manifest, fmt.Errorf("oracle public key does not match the pinned ratification key")
	}
	signature, err := base64.StdEncoding.DecodeString(manifest.Signature.Value)
	if err != nil {
		return manifest, fmt.Errorf("decode oracle signature: %w", err)
	}
	if !ed25519.Verify(pinnedPublicKey, manifest.signingPayload(), signature) {
		return manifest, fmt.Errorf("oracle signature verification failed")
	}
	return manifest, nil
}

func (m Manifest) signingPayload() []byte {
	return []byte(m.Schema + "\n" + m.Fixture.Path + "\n" + m.Fixture.SHA256 + "\n" +
		m.Golden.Path + "\n" + m.Golden.SHA256 + "\n")
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
