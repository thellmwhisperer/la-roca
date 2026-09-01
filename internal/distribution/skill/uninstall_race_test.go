package skill

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
)

func writeManagedSkill(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "skills", SkillName, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	managed := Content()
	if err := os.WriteFile(path, []byte(managed), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, managed
}

func TestUninstallPreservesReplacementPublishedAfterQuarantine(t *testing.T) {
	path, managed := writeManagedSkill(t)
	operator := []byte("operator replacement\n")
	outcome, err := uninstallWithChecksum(agentcfg.RuntimeZcode, path, artifact.Checksum(managed), func() {
		if err := os.WriteFile(path, operator, 0o600); err != nil {
			t.Fatal(err)
		}
	}, os.Remove)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Changed {
		t.Fatal("managed preimage was not withdrawn")
	}
	for _, removed := range outcome.Removed {
		if removed == path {
			t.Fatalf("live operator path reported removed: %v", outcome.Removed)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != string(operator) {
		t.Fatalf("operator replacement changed: body=%q err=%v", body, err)
	}
}

func TestUninstallRestoresQuarantineWhenRemovalFails(t *testing.T) {
	path, managed := writeManagedSkill(t)
	failure := errors.New("synthetic removal failure")
	outcome, err := uninstallWithChecksum(agentcfg.RuntimeZcode, path, artifact.Checksum(managed), nil,
		func(string) error { return failure })
	if !errors.Is(err, failure) {
		t.Fatalf("removal error = %v", err)
	}
	if outcome.Changed {
		t.Fatalf("failed removal changed outcome: %+v", outcome)
	}
	body, readErr := os.ReadFile(path)
	if readErr != nil || string(body) != managed {
		t.Fatalf("canonical skill was not restored: body=%q err=%v", body, readErr)
	}
}
