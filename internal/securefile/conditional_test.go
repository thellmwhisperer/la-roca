package securefile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConditionalReplacementRefusesUnconditionalPrimitive(t *testing.T) {
	for _, api := range []string{"Replace", "ReplaceRegular"} {
		t.Run(api, func(t *testing.T) {
			path, previous := secureFileFixture(t, "config.json", "operator")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			realRename := renameReplaceFile
			t.Cleanup(func() { renameReplaceFile = realRename })
			renameReplaceFile = func(staged, target string) error {
				if err := os.WriteFile(target, []byte("concurrent operator"), 0o600); err != nil {
					return err
				}
				return realRename(staged, target)
			}
			switch api {
			case "Replace":
				err = Replace(path, []byte("candidate"), previous)
			case "ReplaceRegular":
				err = ReplaceRegular(path, []byte("candidate"), previous, info)
			}
			if !errors.Is(err, ErrConditionalReplaceUnsupported) {
				t.Fatalf("error = %v, want unsupported refusal without publication", err)
			}
			assertFileContentAndMode(t, path, previous, 0o600)
			current, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(info, current) {
				t.Fatal("refusal changed the operator's inode")
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 {
				t.Fatalf("refusal left artifacts: %v, %v", entries, err)
			}
		})
	}
}

func TestReplacePreservesSaveAfterValidation(t *testing.T) {
	path, previous := secureFileFixture(t, "config.json", "old")
	realHook := beforePublication
	t.Cleanup(func() { beforePublication = realHook })
	beforePublication = func(target string) {
		if err := os.WriteFile(target, []byte("operator save"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Replace(path, []byte("candidate"), previous); !errors.Is(err, ErrConditionalReplaceUnsupported) {
		t.Fatalf("replace = %v, want unsupported refusal", err)
	}
	assertFileContentAndMode(t, path, []byte("operator save"), 0o600)
}

func TestMissingConditionalReplacementRefusesIdenticalCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.json")
	data := []byte("generated")
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	realHook := beforePublication
	t.Cleanup(func() { beforePublication = realHook })
	beforePublication = func(string) {
		entered <- struct{}{}
		<-release
	}
	results := make(chan error, 2)
	go func() { results <- Replace(path, data, nil) }()
	go func() { results <- CreatePreservingParentMode(path, data, 0o600, 0o700) }()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("both writers must reach publication before either proceeds")
		}
	}
	once.Do(func() { close(release) })
	successes, collisions := 0, 0
	for range 2 {
		select {
		case err := <-results:
			if err == nil {
				successes++
			} else if strings.Contains(err.Error(), "existing file was preserved") {
				collisions++
			} else {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("publication did not finish")
		}
	}
	if successes != 1 || collisions != 1 {
		t.Fatalf("successes = %d, collisions = %d", successes, collisions)
	}
	assertFileContentAndMode(t, path, data, 0o600)
}

func TestWriteFailureLeavesTheLivePath(t *testing.T) {
	path, previous := secureFileFixture(t, "config.json", "old")
	realRename := renameReplaceFile
	t.Cleanup(func() { renameReplaceFile = realRename })
	failure := errors.New("injected publication failure")
	renameReplaceFile = func(_, target string) error {
		assertFileContentAndMode(t, target, previous, 0o600)
		return failure
	}
	if err := Write(path, []byte("new"), 0o600, 0o700); !errors.Is(err, failure) {
		t.Fatalf("write = %v, want injected failure", err)
	}
	assertFileContentAndMode(t, path, previous, 0o600)
}

func TestWriteStillReplacesUnconditionally(t *testing.T) {
	path, _ := secureFileFixture(t, "config.json", "old")
	if err := Write(path, []byte("new"), 0o600, 0o700); err != nil {
		t.Fatal(err)
	}
	assertFileContentAndMode(t, path, []byte("new"), 0o600)
}

func secureFileFixture(t *testing.T, name, content string) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	previous := []byte(content)
	if err := os.WriteFile(path, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, previous
}
