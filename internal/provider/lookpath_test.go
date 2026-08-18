package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLookPathFindsAWellKnownBinWhenPATHMisses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/synthetic/empty-path")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(bin, "cursor-agent")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

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
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "/synthetic/empty-path")
	if _, err := LookPath("cursor-agent-missing-fixture"); err == nil {
		t.Fatal("LookPath invented a binary that is not on PATH or in a well-known bin")
	}
}
