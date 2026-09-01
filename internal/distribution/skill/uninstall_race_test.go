package skill

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
)

func TestUninstallPreservesReplacementPublishedAfterQuarantine(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "skills", SkillName, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	managed := Content()
	if err := os.WriteFile(path, []byte(managed), 0o600); err != nil {
		t.Fatal(err)
	}
	operator := []byte("operator replacement\n")
	outcome, err := uninstallWithChecksum(agentcfg.RuntimeZcode, path, artifact.Checksum(managed), func() {
		if err := os.WriteFile(path, operator, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Changed {
		t.Fatal("managed preimage was not withdrawn")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != string(operator) {
		t.Fatalf("operator replacement changed: body=%q err=%v", body, err)
	}
}
