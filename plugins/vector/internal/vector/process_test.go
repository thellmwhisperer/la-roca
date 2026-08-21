package vector

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceFilePublishesAndReplaces(t *testing.T) {
	for _, destinationExists := range []bool{false, true} {
		name := "publish"
		if destinationExists {
			name = "replace"
		}
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			source := filepath.Join(directory, "source")
			destination := filepath.Join(directory, "destination")
			if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
				t.Fatal(err)
			}
			if destinationExists {
				if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if err := replaceFile(source, destination); err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(destination)
			if err != nil {
				t.Fatal(err)
			}
			if string(contents) != "new" {
				t.Fatalf("destination contents = %q, want new", contents)
			}
			if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("source remains after replacement: %v", err)
			}
		})
	}
}
