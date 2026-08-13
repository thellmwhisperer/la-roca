package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
)

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
	zones, err := artifact.Parse(string(body))
	if err != nil || zones.User != "" {
		t.Fatalf("prompt zones = %+v, err %v", zones, err)
	}
	for _, want := range []string{
		"La Roca", "local semantic memory", "when to query",
		"roca query \"<natural question>\"", "roca store",
		"Data = `roca query`; human reading = `roca query --full`; raw SQL = `roca exec`.",
		"roca_query", "roca_store", "--agent", "--model", "authorship",
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

func TestInitAdoptsAnUnrecognizedLegacyPromptWithoutLosingIt(t *testing.T) {
	paths := freshPaths(t)
	path := filepath.Join(paths.data, "prompt.md")
	if err := os.WriteFile(path, []byte("operator legacy prompt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := serviceOn(t, paths)
	result, err := svc.Init(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	zones, err := artifact.Parse(result.Prompt)
	if err != nil {
		t.Fatal(err)
	}
	if zones.User != "operator legacy prompt\n" {
		t.Fatalf("legacy prompt was not preserved verbatim: %q", zones.User)
	}
}

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
