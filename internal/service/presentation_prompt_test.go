/*
*
@overview Init-generated agent presentation prompt contract. ~105 lines, no public symbols.

	READING GUIDE
	-------------
	1. Start at TestInitWritesTheAgentPresentationPrompt
	2. TestDoctorDistinguishesAMissingPrompt pins the pre-init/legacy diagnosis

	MAIN FLOW
	---------
	Service.Init -> prompt.md in data directory -> InitResult and DoctorReport references

	PUBLIC API
	----------
	None; this file tests service behavior.

	INTERNALS
	---------
	TestInitWritesTheAgentPresentationPrompt, TestDoctorDistinguishesAMissingPrompt

@exports
@deps os/path/filepath/strings/testing, internal/service
*/
package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -- 1/2 CORE · TestInitWritesTheAgentPresentationPrompt -- <- START HERE

func TestInitWritesTheAgentPresentationPrompt(t *testing.T) {
	paths := freshPaths(t)
	svc := serviceOn(t, paths)
	result, err := svc.Init(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(paths.data, "prompt.md")
	if result.PromptPath != wantPath {
		t.Fatalf("prompt path = %q, want %q", result.PromptPath, wantPath)
	}
	body, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read generated prompt: %v", err)
	}
	if result.Prompt != string(body) {
		t.Error("init did not return the exact prompt it wrote")
	}
	for _, want := range []string{
		"La Roca", "local semantic memory", "when to query",
		"roca query \"<natural question>\"", "roca store",
		"roca_query", "roca_store",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("prompt does not carry %q:\n%s", want, body)
		}
	}
	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("prompt mode = %o, want 600", info.Mode().Perm())
	}
	report, err := svc.Doctor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.PromptPath != wantPath {
		t.Fatalf("doctor prompt path = %q, want %q", report.PromptPath, wantPath)
	}
	if !report.PromptExists {
		t.Fatal("doctor says the generated prompt is missing")
	}
}

// -/ 1/2

// -- 2/2 CORE · TestDoctorDistinguishesAMissingPrompt --

func TestDoctorDistinguishesAMissingPrompt(t *testing.T) {
	paths := freshPaths(t)
	svc := serviceOn(t, paths)
	if _, err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(paths.data, "prompt.md")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	report, err := svc.Doctor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.PromptExists {
		t.Fatal("doctor advertises a prompt file that is missing")
	}
	if report.PromptPath != path {
		t.Fatalf("doctor lost the missing prompt location: %q", report.PromptPath)
	}
}

// -/ 2/2
