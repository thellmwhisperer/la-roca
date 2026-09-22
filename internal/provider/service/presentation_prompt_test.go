package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
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
		"The optional playground plugin provides human answers with `roca playground --full` and investigations with `roca explore`.",
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

func TestInitPreservesAnEarlierReleasesPromptOnRefusal(t *testing.T) {
	paths := freshPaths(t)
	path := filepath.Join(paths.data, "prompt.md")
	earlier := service.PresentationPromptSignature() + "what an older release said\n"
	if err := os.WriteFile(path, []byte(earlier), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := serviceOn(t, paths).Init(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Prompt != "" || result.PromptPath != "" {
		t.Fatalf("refused replacement advertised a newly installed prompt: %q", result.Prompt)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != earlier {
		t.Fatalf("refused replacement changed live prompt: %q, err %v", body, err)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "atomic conditional replacement is unsupported") {
		t.Fatalf("init did not report conditional refusal: %q", result.Warnings)
	}
	// The backup survives even when publication is refused.
	backup := path + ".roca.bak"
	if kept, err := os.ReadFile(backup); err != nil || string(kept) != earlier {
		t.Fatalf("the migration kept no recovery copy of the previous prompt: %q, err %v", kept, err)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), backup) {
		t.Fatalf("init did not name the recovery copy it made: %q", result.Warnings)
	}
}

func TestInitPreservesAnUnrecognizedLegacyPromptOnRefusal(t *testing.T) {
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
	if result.Prompt != "" || result.PromptPath != "" {
		t.Fatalf("refused replacement advertised a newly installed prompt: %q", result.Prompt)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "operator legacy prompt\n" {
		t.Fatalf("refused replacement changed live prompt: %q, err %v", body, err)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "atomic conditional replacement is unsupported") {
		t.Fatalf("init did not report conditional refusal: %q", result.Warnings)
	}
	// The refusal must name the recovery copy without claiming replacement.
	warnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(warnings, path+".roca.bak") || strings.Contains(warnings, "replaced") {
		t.Fatalf("init misdescribed an adopted prompt: %q", warnings)
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
