package securefile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReplaceWithResultCarriesPublishedIdentity(t *testing.T) {
	path, previous := secureFileFixture(t, "config.json", "operator configuration")

	publication, err := ReplaceWithResult(path, []byte("managed configuration"), previous)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if !publication.Identity.Valid() || !publication.Identity.Matches(path) {
		t.Fatalf("publication identity does not name the published path")
	}

	// A byte-identical operator replacement has a different inode. The result
	// must not be inferred from a later path sample by cleanup code.
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
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "managed configuration" {
		t.Fatalf("content = %q, want concurrent operator bytes", content)
	}
}

func TestConditionalReplacementsSerializeAtPublication(t *testing.T) {
	path, previous := secureFileFixture(t, "config.json", "old")
	entered, release := blockFirstPublication(t)

	first := make(chan error, 1)
	go func() { first <- Replace(path, []byte("first"), previous) }()
	<-entered

	second := make(chan error, 1)
	go func() { second <- Replace(path, []byte("operator"), previous) }()
	select {
	case err := <-second:
		t.Fatalf("concurrent replacement completed before publication: %v", err)
	default:
	}
	close(release)

	if err := <-first; err != nil {
		t.Fatalf("first replacement: %v", err)
	}
	if err := <-second; err == nil || !strings.Contains(err.Error(), "changed while it was being edited") {
		t.Fatalf("second replacement error = %v, want conditional refusal", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "first" {
		t.Fatalf("content = %q, want first publication preserved", content)
	}
}

func TestMissingConditionalReplacementRefusesIdenticalCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.json")
	data := []byte("generated")
	entered, release := blockFirstPublication(t)

	first := make(chan error, 1)
	go func() { first <- CreatePreservingParentMode(path, data, 0o600, 0o700) }()
	<-entered
	second := make(chan error, 1)
	go func() { second <- CreatePreservingParentMode(path, data, 0o600, 0o700) }()
	select {
	case err := <-second:
		t.Fatalf("collision completed before publication: %v", err)
	default:
	}
	close(release)

	if err := <-first; err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := <-second; err == nil || !strings.Contains(err.Error(), "existing file was preserved") {
		t.Fatalf("second create error = %v, want preserved collision", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(data) {
		t.Fatalf("content = %q, want generated", content)
	}
}

func TestReplaceRefusesWithoutAtomicPrimitive(t *testing.T) {
	path, previous := secureFileFixture(t, "config.json", "old")

	realRename := renameReplaceFile
	t.Cleanup(func() { renameReplaceFile = realRename })
	renameReplaceFile = func(_, _ string) error { return errAtomicReplaceUnsupported }
	if err := Replace(path, []byte("new"), previous); err == nil ||
		!strings.Contains(err.Error(), "atomic replacement publication is unsupported") {
		t.Fatalf("replace error = %v, want unsupported-publication refusal", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(previous) {
		t.Fatalf("content = %q, want old", content)
	}
}

func TestReplaceFailureLeavesTheLivePath(t *testing.T) {
	path, previous := secureFileFixture(t, "config.json", "old")

	realRename := renameReplaceFile
	t.Cleanup(func() { renameReplaceFile = realRename })
	renameReplaceFile = func(_, target string) error {
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("live path was absent before replacement: %v", err)
		}
		return errors.New("injected publication failure")
	}
	if err := Replace(path, []byte("new"), previous); err == nil ||
		!strings.Contains(err.Error(), "injected publication failure") {
		t.Fatalf("replace error = %v, want injected failure", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(previous) {
		t.Fatalf("content = %q, want old", content)
	}
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

func blockFirstPublication(t *testing.T) (chan struct{}, chan struct{}) {
	t.Helper()
	realHook := beforePublication
	t.Cleanup(func() { beforePublication = realHook })
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	beforePublication = func(string) {
		if calls.Add(1) != 1 {
			return
		}
		close(entered)
		<-release
	}
	return entered, release
}
