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

func TestReplaceWithResultCarriesPublishedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	publication, err := ReplaceWithResult(path, []byte("managed configuration"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !publication.Identity.Valid() || !publication.Identity.Matches(path) {
		t.Fatal("publication identity does not name the published path")
	}
	operator := filepath.Join(filepath.Dir(path), ".operator")
	if err := os.WriteFile(operator, []byte("managed configuration"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(operator, path); err != nil {
		t.Fatal(err)
	}
	if publication.Identity.Matches(path) {
		t.Fatal("published identity claimed a later byte-identical replacement")
	}
	if _, err := ReplaceWithIdentity(path, []byte("rollback"), []byte("managed configuration"), publication.Identity); err == nil {
		t.Fatal("rollback accepted a later byte-identical inode")
	}
	assertFileContentAndMode(t, path, []byte("managed configuration"), 0o600)
}

func TestConditionalReplacementRefusesUnconditionalPrimitive(t *testing.T) {
	for _, api := range []string{"Replace", "ReplaceWithResult", "ReplaceRegular", "ReplaceRegularWithResult", "ReplaceWithIdentity"} {
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
			var result Publication
			switch api {
			case "Replace":
				err = Replace(path, []byte("candidate"), previous)
			case "ReplaceWithResult":
				result, err = ReplaceWithResult(path, []byte("candidate"), previous)
			case "ReplaceRegular":
				err = ReplaceRegular(path, []byte("candidate"), previous, info)
			case "ReplaceRegularWithResult":
				result, err = ReplaceRegularWithResult(path, []byte("candidate"), previous, info)
			case "ReplaceWithIdentity":
				result, err = ReplaceWithIdentity(path, []byte("candidate"), previous, identityFromInfo(info))
			}
			if !errors.Is(err, ErrConditionalReplaceUnsupported) || result.Identity.Valid() {
				t.Fatalf("result = %+v, error = %v, want unsupported refusal without publication", result, err)
			}
			assertFileContentAndMode(t, path, previous, 0o600)
			if !identityFromInfo(info).Matches(path) {
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
