package bundledplugin_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

func TestSweepStagingRecordsFolderFailuresAndContinues(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix permissions without root privileges")
	}
	for _, failure := range []string{"inspection", "lock", "removal"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			stuck := filepath.Join(root, ".alpha-bundle-a-stuck")
			good := filepath.Join(root, ".alpha-bundle-z-removable")
			sub := filepath.Join(stuck, "sub")
			for _, path := range []string{sub, good} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(sub, "payload"), []byte("12345"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Chmod(sub, 0o700); err != nil {
					t.Error(err)
				}
			})
			if failure == "lock" {
				if err := os.Mkdir(filepath.Join(stuck, ".held"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			old := time.Now().Add(-2 * time.Hour)
			for _, path := range []string{filepath.Join(sub, "payload"), sub, stuck, good} {
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "lock" {
				if err := os.Chtimes(filepath.Join(stuck, ".held"), old, old); err != nil {
					t.Fatal(err)
				}
			} else {
				mode := os.FileMode(0o500)
				if failure == "inspection" {
					mode = 0
				}
				if err := os.Chmod(sub, mode); err != nil {
					t.Fatal(err)
				}
			}
			folders, err := bundledplugin.SweepStaging(root, "alpha", true)
			if err != nil || len(folders) != 2 {
				t.Fatalf("sweep = %+v, %v", folders, err)
			}
			if folders[0].Path != stuck || folders[0].Cause == "" || folders[0].Removed {
				t.Fatalf("failure not recorded: %+v", folders[0])
			}
			if failure == "removal" && (folders[0].Size != 5 || !strings.Contains(folders[0].Cause, "permission denied")) {
				t.Fatalf("removal diagnosis = %+v", folders[0])
			}
			if folders[1].Path != good || !folders[1].Removed || folders[1].Cause != "" {
				t.Fatalf("sweep did not continue: %+v", folders[1])
			}
		})
	}
}

func TestSweepStagingKeepsAgeAndLocksAcrossFolderFailures(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix permissions without root privileges")
	}
	for _, kind := range []string{"bundle", "schema-preflight"} {
		for _, failure := range []string{"unreadable", "partial removal", "nested partial removal", "held inspection"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				root := t.TempDir()
				directory := filepath.Join(root, ".alpha-"+kind+"-stale")
				parent := directory
				if failure == "nested partial removal" {
					parent = filepath.Join(directory, "branch")
				}
				protected := filepath.Join(parent, "sub")
				if err := os.MkdirAll(protected, 0o700); err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{filepath.Join(directory, ".held"), filepath.Join(parent, "removed"), filepath.Join(protected, "payload")} {
					if err := os.WriteFile(path, nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if failure == "held inspection" {
					release, err := securefile.Lock(filepath.Join(directory, ".held"))
					if err != nil {
						t.Fatal(err)
					}
					defer release()
				}
				ageTree(t, directory)
				mode := os.FileMode(0o500)
				if failure == "unreadable" {
					protected = directory
				}
				if failure == "unreadable" || failure == "held inspection" {
					mode = 0
				}
				if err := os.Chmod(protected, mode); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(protected, 0o700); err != nil {
						t.Error(err)
					}
				})
				for _, remove := range []bool{false, true, true, false} {
					folders, err := bundledplugin.SweepStaging(root, "alpha", remove)
					if err != nil || len(folders) != 1 {
						t.Fatalf("sweep = %+v, %v", folders, err)
					}
					folder := folders[0]
					if folder.Age < time.Hour || folder.Removed || folder.Held != (failure == "held inspection") {
						t.Fatalf("stale folder age or lock lost: %+v", folder)
					}
					if remove && !strings.Contains(folder.Cause, "permission denied") {
						t.Fatalf("failure not diagnosed: %+v", folder)
					}
					if remove && strings.Contains(failure, "partial removal") {
						if _, err := os.Stat(filepath.Join(directory, ".held")); !os.IsNotExist(err) {
							t.Fatalf("unlocked .held was not removed: %v", err)
						}
						if _, err := os.Stat(filepath.Join(parent, "removed")); !os.IsNotExist(err) {
							t.Fatalf("removable sibling remains: %v", err)
						}
					}
				}
			})
		}
	}
}

func TestSweepStagingPreservesRecentDirectoryAndFileActivity(t *testing.T) {
	for _, kind := range []string{"bundle", "schema-preflight"} {
		for _, recent := range []string{"directory", "subdirectory", "file", "file after inspection failure"} {
			t.Run(kind+"/"+recent, func(t *testing.T) {
				root := t.TempDir()
				directory := filepath.Join(root, ".alpha-"+kind+"-active")
				sub := filepath.Join(directory, "sub")
				payload := filepath.Join(sub, "payload")
				if err := os.MkdirAll(sub, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(payload, []byte("active"), 0o600); err != nil {
					t.Fatal(err)
				}
				paths := []string{payload, sub, directory}
				if recent == "file after inspection failure" {
					if runtime.GOOS == "windows" || os.Geteuid() == 0 {
						t.Skip("requires Unix permissions without root privileges")
					}
					blocked := filepath.Join(directory, "a-unreadable")
					if err := os.Mkdir(blocked, 0); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := os.Chmod(blocked, 0o700); err != nil {
							t.Error(err)
						}
					})
					paths = append(paths, blocked)
				}
				old := time.Now().Add(-2 * time.Hour)
				for _, path := range paths {
					if err := os.Chtimes(path, old, old); err != nil {
						t.Fatal(err)
					}
				}
				path := map[string]string{"directory": directory, "subdirectory": sub, "file": payload, "file after inspection failure": payload}[recent]
				now := time.Now()
				if err := os.Chtimes(path, now, now); err != nil {
					t.Fatal(err)
				}
				for _, remove := range []bool{false, true} {
					folders, err := bundledplugin.SweepStaging(root, "alpha", remove)
					if err != nil || len(folders) != 1 {
						t.Fatalf("sweep = %+v, %v", folders, err)
					}
					folder := folders[0]
					if folder.Age > time.Hour || folder.Removed || (folder.Cause != "") != (recent == "file after inspection failure") {
						t.Fatalf("recent activity ignored: %+v", folder)
					}
					if _, err := os.Stat(payload); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestSweepStagingCountsSurvivingBytesAfterPartialRemoval(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix permissions without root privileges")
	}
	for _, kind := range []string{"bundle", "schema-preflight"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, ".alpha-"+kind+"-leftover")
			sub := filepath.Join(directory, "sub")
			if err := os.MkdirAll(sub, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "removable"), []byte("deleted payload"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sub, "payload"), []byte("12345"), 0o600); err != nil {
				t.Fatal(err)
			}
			ageTree(t, directory)
			if err := os.Chmod(sub, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Chmod(sub, 0o700); err != nil {
					t.Error(err)
				}
			})
			folders, err := bundledplugin.SweepStaging(root, "alpha", true)
			if err != nil || len(folders) != 1 || folders[0].Size != 5 {
				t.Fatalf("surviving size = %+v, %v; want 5 bytes", folders, err)
			}
		})
	}
}

// ageTree makes every entry under directory two hours old.
func ageTree(t *testing.T, directory string) {
	t.Helper()
	old := time.Now().Add(-2 * time.Hour)
	if err := filepath.Walk(directory, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, old, old)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSweepStagingReturnsPluginRootFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(root, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bundledplugin.SweepStaging(root, "alpha", true); err == nil {
		t.Fatal("plugin root failure was ignored")
	}
}

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
	release, err := securefile.Lock(filepath.Join(held, ".held"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	old := time.Now().Add(-2 * time.Hour)
	for _, directory := range []string{filepath.Join(stale, "alpha.db"), filepath.Join(held, ".held"), stale, held} {
		if err := os.Chtimes(directory, old, old); err != nil {
			t.Fatal(err)
		}
	}
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

func TestSweepStagingPreservesFreshFolders(t *testing.T) {
	for _, kind := range []string{"bundle", "schema-preflight"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, ".alpha-"+kind+"-fresh")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			folders, err := bundledplugin.SweepStaging(root, "alpha", true)
			if err != nil || len(folders) != 1 {
				t.Fatalf("sweep = %+v, %v", folders, err)
			}
			if folders[0].Removed || folders[0].Held || folders[0].Age > time.Hour {
				t.Fatalf("fresh folder = %+v", folders[0])
			}
			if _, err := os.Stat(directory); err != nil {
				t.Fatal(err)
			}
		})
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
