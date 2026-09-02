package skill_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/distribution/skill"
)

func TestDetectedDoesNotAutoInstallTheOptInZcodeSkills(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".zcode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := skill.Detected(home, nil); len(got) != 0 {
		t.Fatalf("detected = %v, want no opt-in-only zcode runtime", got)
	}
	path, err := skill.Path(agentcfg.RuntimeZcode, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".zcode", "skills", skill.SkillName, "SKILL.md")
	if path != want {
		t.Fatalf("zcode skill path = %q, want %q", path, want)
	}
}
