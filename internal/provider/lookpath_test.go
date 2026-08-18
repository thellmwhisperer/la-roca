package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func isolatedLookPathHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/synthetic/empty-path")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	return home, bin
}

func writeLookPathFixture(t *testing.T, bin, name string) string {
	t.Helper()
	path := filepath.Join(bin, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLookPathFindsAWellKnownBinWhenPATHMisses(t *testing.T) {
	_, bin := isolatedLookPathHome(t)
	tool := writeLookPathFixture(t, bin, "cursor-agent")

	got, err := LookPath("cursor-agent")
	if err != nil {
		t.Fatalf("LookPath: %v", err)
	}
	if got != tool {
		t.Fatalf("LookPath = %s, want %s", got, tool)
	}
	if !BinaryOnPath(nil, "cursor-agent") {
		t.Fatal("BinaryOnPath missed the well-known cursor-agent")
	}
}

func TestLookPathDoesNotInventAMissingBinary(t *testing.T) {
	isolatedLookPathHome(t)
	if _, err := LookPath("cursor-agent-missing-fixture"); err == nil {
		t.Fatal("LookPath invented a binary that is not on PATH or in a well-known bin")
	}
}

func TestLookPathDoesNotReinterpretAnExplicitCommandPath(t *testing.T) {
	_, bin := isolatedLookPathHome(t)
	writeLookPathFixture(t, bin, "claude")
	if _, err := LookPath("./claude"); err == nil {
		t.Fatal("LookPath replaced an explicit missing path with a well-known-bin command")
	}
}
