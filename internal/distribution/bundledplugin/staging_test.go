package bundledplugin_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

func TestSweepStagingListsSizeAgeAndLocksBeforeRemoval(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, ".alpha-schema-preflight-stale")
	held := filepath.Join(root, ".alpha-bundle-live")
	for _, directory := range []string{stale, held, filepath.Join(root, "alpha"), filepath.Join(root, ".beta-bundle-stale")} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stale, "alpha.db"), []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	release, err := securefile.Lock(filepath.Join(held, ".held"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	listed, err := bundledplugin.SweepStaging(root, "alpha", false)
	if err != nil || len(listed) != 2 {
		t.Fatalf("list = %+v, %v", listed, err)
	}
	for _, folder := range listed {
		if folder.Removed || folder.Held != (folder.Path == held) {
			t.Fatalf("list changed a folder or misreported its lock: %+v", folder)
		}
		if folder.Path == stale && (folder.Size != int64(len("database")) || folder.Age < time.Hour) {
			t.Fatalf("size/age = %+v", folder)
		}
		if _, err := os.Stat(folder.Path); err != nil {
			t.Fatal(err)
		}
	}
	swept, err := bundledplugin.SweepStaging(root, "alpha", true)
	if err != nil || len(swept) != 2 {
		t.Fatalf("sweep = %+v, %v", swept, err)
	}
	for _, folder := range swept {
		if folder.Removed != (folder.Path == stale) {
			t.Fatalf("removal result = %+v", folder)
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale directory remains: %v", err)
	}
	for _, directory := range []string{held, filepath.Join(root, "alpha"), filepath.Join(root, ".beta-bundle-stale")} {
		if _, err := os.Stat(directory); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnsureHoldsBothKindsOfStagingLock(t *testing.T) {
	root, bin := t.TempDir(), t.TempDir()
	spec := dataSpec("alpha")
	apply := spec.ApplySchema
	kinds := map[bool]bool{}
	spec.ApplySchema = func(path string) error {
		directory := filepath.Dir(path)
		if !strings.HasPrefix(filepath.Base(directory), ".alpha-") {
			t.Fatalf("schema fixture is outside staging: %s", directory)
		}
		unlock, err := securefile.TryLock(filepath.Join(directory, ".held"))
		if unlock != nil {
			_ = unlock()
		}
		if !errors.Is(err, securefile.ErrHeld) {
			t.Fatalf("staging lock is not held during ApplySchema: %v", err)
		}
		folders, err := bundledplugin.SweepStaging(root, "alpha", true)
		if err != nil {
			t.Fatal(err)
		}
		for _, folder := range folders {
			if !folder.Held || folder.Removed {
				t.Fatalf("active staging was swept: %+v", folder)
			}
		}
		kinds[strings.Contains(directory, "-schema-preflight-")] = true
		return apply(path)
	}
	if _, err := bundledplugin.Ensure(root, bin, "v1", spec); err != nil {
		t.Fatal(err)
	}
	if _, err := bundledplugin.EnsureAll(root, bin, "v1", spec); err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 2 {
		t.Fatalf("did not exercise bundle and preflight: %v", kinds)
	}
	folders, err := bundledplugin.SweepStaging(root, "alpha", false)
	if err != nil || len(folders) != 0 {
		t.Fatalf("staging cleanup = %+v, %v", folders, err)
	}
}
