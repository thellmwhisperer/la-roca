package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A public product must not leak the internal factory/fleet workflow that built
// it. These names belong to that workflow and to nothing a user, a contributor
// or a doc reader should ever see. If one returns, this test fails the build.
//
// "scout" is the one word here that also lives in a legitimate idiom, Uncle
// Bob's boy-scout rule (leave the code cleaner than you found it). It is allowed
// inside that idiom and forbidden everywhere else, which is exactly "scout as a
// factory role is banned; the clean-code idiom is not".
var forbiddenFactoryTerms = []struct {
	term    string
	forgive string // lowercased compound that pardons an occurrence of term
}{
	{term: "captain"},
	{term: "firstmate"},
	{term: "crewmate"},
	{term: "secondmate"},
	{term: "no-mistakes"},
	{term: "shipshape"},
	{term: "yolo"},
	{term: "fleet scout"},
	{term: "roca-madre"},
	{term: "roca_madre"},
	{term: "rocamadre"},
	{term: "scout", forgive: "boy-scout"},
}

// directories that are not product source: build output, local agent state, and
// vendored dependencies. none of them ships in the repository.
var nonProductDirs = map[string]bool{
	".git": true, ".tmp": true, ".worktrees": true,
	"bin": true, "dist": true, "vendor": true,
	"node_modules": true, ".opencode": true,
}

func TestProductVocabularyIsFreeOfInternalFactoryRoles(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	here = filepath.Clean(here)
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "..", "..", ".."))

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if nonProductDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		// A worktree keeps its git pointer in a regular file named ".git"; a
		// normal checkout keeps it in a directory (already skipped above).
		if name == ".git" {
			return nil
		}
		// This file is the fixture that asserts the ban, so it is allowed to
		// name the terms it forbids.
		if path == here {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(body, 0) >= 0 {
			return nil // binary
		}
		lower := strings.ToLower(string(body))
		for _, f := range forbiddenFactoryTerms {
			for pos := 0; ; {
				i := strings.Index(lower[pos:], f.term)
				if i < 0 {
					break
				}
				at := pos + i
				if !forgiven(lower, at, f.term, f.forgive) {
					t.Errorf("%s: forbidden term %q", path, f.term)
				}
				pos = at + len(f.term)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// forgiven reports whether the occurrence of term at at is part of the allowed
// compound (for example "scout" inside "boy-scout").
func forgiven(lower string, at int, term, compound string) bool {
	if compound == "" {
		return false
	}
	i := strings.Index(compound, term)
	start := at - i
	end := at + len(term) + (len(compound) - i - len(term))
	return start >= 0 && end <= len(lower) && lower[start:end] == compound
}
