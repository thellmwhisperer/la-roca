package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVerificationInvalidatesChangedFile(t *testing.T) {
	payload := []byte("synthetic verified model")
	sum := sha256.Sum256(payload)
	manifest := Manifest{ID: "fixture", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(payload)), URL: "https://example.invalid/model"}
	for _, change := range []string{"mtime", "size", "inode", "missing", "checksum"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			path := FilePath(root, manifest)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if got, err := Existing(root, manifest); err != nil || got != path {
					t.Fatalf("existing: %q %v", got, err)
				}
				if got, err := Ensure(context.Background(), root, manifest, nil); err != nil || got != path {
					t.Fatalf("ensure: %q %v", got, err)
				}
			}
			corrupt := append([]byte(nil), payload...)
			corrupt[0] ^= 1
			switch change {
			case "mtime":
				if err := os.WriteFile(path, corrupt, 0600); err != nil {
					t.Fatal(err)
				}
				changed := info.ModTime().Add(time.Second)
				if err := os.Chtimes(path, changed, changed); err != nil {
					t.Fatal(err)
				}
			case "size":
				if err := os.WriteFile(path, append(payload, 0), 0600); err != nil {
					t.Fatal(err)
				}
			case "inode":
				replacement := path + ".replacement"
				if err := os.WriteFile(replacement, corrupt, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				// Copy the old receipt too: size and mtime equality cannot hide a new inode.
				original, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				receipt := readVerification(original)
				original.Close()
				fresh, err := os.Open(replacement)
				if err != nil {
					t.Fatal(err)
				}
				writeVerification(fresh, receipt)
				fresh.Close()
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "checksum":
				if err := verifyFile(path, string(make([]byte, 64)), manifest.Bytes); err == nil {
					t.Fatal("accepted changed checksum")
				}
				return
			}
			if _, err := Existing(root, manifest); err == nil {
				t.Fatal("accepted changed model")
			}
		})
	}
}
