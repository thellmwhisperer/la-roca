package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/lifecycle"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestThePurgeOwnsTheGeneratedAgentPrompt(t *testing.T) {
	paths := resolvedIn(t, t.TempDir())
	prompt := filepath.Join(dirOf(paths.DB), "prompt.md")
	if err := os.MkdirAll(filepath.Dir(prompt), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prompt, []byte(service.PresentationPrompt()), 0o600); err != nil {
		t.Fatal(err)
	}

	report := lifecycle.Plan{Owned: ownedPaths(paths), DataDir: dirOf(paths.DB)}.Apply()
	if _, err := os.Stat(prompt); !os.IsNotExist(err) {
		t.Fatalf("generated prompt survived purge: %v", err)
	}
	for _, kept := range report.Kept {
		if kept.Path == prompt {
			t.Fatalf("generated prompt reported as operator-owned: %s", kept.Reason)
		}
	}
}

// A prompt.md an older release generated is still a file init wrote. Owning
// only this release's exact bytes left it behind and reported it as somebody
// else's, which is a false statement about a file this product created.
func TestThePurgeOwnsAnEarlierReleasesPrompt(t *testing.T) {
	paths := resolvedIn(t, t.TempDir())
	prompt := filepath.Join(dirOf(paths.DB), "prompt.md")
	earlier := service.PresentationPromptSignature() + "what an older release said\n"
	for _, body := range []string{earlier, artifact.Zoned(earlier, "")} {
		writeFile(t, prompt, body)
		if !slices.Contains(ownedPaths(paths), prompt) {
			t.Fatalf("the purge disowned a prompt this product generated: %q", body)
		}
	}
}

func TestThePurgeDoesNotClaimAnOperatorPromptZone(t *testing.T) {
	paths := resolvedIn(t, t.TempDir())
	prompt := filepath.Join(dirOf(paths.DB), "prompt.md")
	writeFile(t, prompt, artifact.Zoned(service.PresentationPrompt(), "keep me\n"))
	if slices.Contains(ownedPaths(paths), prompt) {
		t.Fatal("the central inventory claimed an operator-owned prompt zone")
	}
}

func TestCentralOwnershipIncludesSafeRegistryArtifacts(t *testing.T) {
	home := t.TempDir()
	paths := resolvedIn(t, home)
	skillPath := filepath.Join(home, ".codex", "skills", "roca", "SKILL.md")
	writeFile(t, skillPath, artifact.Zoned("system\n", ""))
	if err := artifact.SaveRegistry(paths.Artifacts, artifact.Registry{Entries: []artifact.Entry{{
		Kind: "skill", Runtime: "codex", Path: skillPath,
		SystemSHA256: artifact.Checksum("system\n"),
	}}}); err != nil {
		t.Fatal(err)
	}
	owned := ownedPaths(paths)
	for _, want := range []string{paths.Artifacts, skillPath} {
		if !slices.Contains(owned, want) {
			t.Errorf("central ownership omitted %s: %v", want, owned)
		}
	}
}

func TestInitRegistersTheVersionedPrompt(t *testing.T) {
	home := t.TempDir()
	isolateRuntimeDirs(t, home)
	db := filepath.Join(home, ".roca", "roca.db")
	runRoot(t, Build{Version: "v1.2.3", Commit: "test"}, "init", "--db-path", db)
	prompt := filepath.Join(home, ".roca", "prompt.md")
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := registry.Find("prompt", "", prompt)
	if !ok || entry.InstalledVersion != "v1.2.3" {
		t.Fatalf("registered prompt = %+v, found %v", entry, ok)
	}
}
