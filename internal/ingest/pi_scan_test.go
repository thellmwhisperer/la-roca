package ingest

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPiScanAccountsForTheCompleteStore(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".pi")
	sessions := filepath.Join(root, "agent", "sessions")
	files := map[string]string{
		"agent/sessions/--synthetic-beacon--/root.jsonl":                      "session",
		"agent/sessions/--synthetic-beacon--/parent/task/run-0/session.jsonl": "session",
		"agent/legacy.jsonl":                        "session",
		"agent/run-history.jsonl":                   "runtime log",
		"agent/missions/index/synthetic.json":       "mission index",
		"agent/prompts/synthetic.md":                "configuration",
		"agent/skills/synthetic/SKILL.md":           "configuration",
		"agent/settings.json":                       "configuration",
		"agent/npm/node_modules/synthetic/index.js": "package",
		"agent/pi-crash.log":                        "runtime log",
	}
	w := &world{home: home}
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		w.write(t, path, content)
	}

	plan := Scan(Roots{Home: home, PiRoot: root, PiSessions: sessions})
	if got := plan.Scanned["pi_files"]; got != len(files) {
		t.Fatalf("Pi files seen = %d, want %d", got, len(files))
	}
	if got := plan.Scanned["pi_session_files"]; got != 3 {
		t.Fatalf("Pi sessions = %d, want 3", got)
	}
	var piTargets int
	for _, target := range plan.Targets {
		if target.SourceAgent == "pi" {
			piTargets++
		}
	}
	if piTargets != 3 {
		t.Errorf("Pi parse targets = %d, want 3", piTargets)
	}
	if len(plan.Excluded) != len(files)-3 {
		t.Fatalf("Pi exclusions = %d, want %d: %+v", len(plan.Excluded), len(files)-3, plan.Excluded)
	}
	reasons := map[string]int{}
	for _, target := range plan.Excluded {
		if target.ExclusionReason == "" {
			t.Errorf("unnamed Pi exclusion: %+v", target)
		}
		reasons[target.ExclusionReason]++
	}
	for _, family := range []string{
		"Pi runtime and package file",
		"Pi configuration file",
		"Pi runtime log",
		"Pi mission index metadata",
	} {
		if reasons[family] == 0 {
			t.Errorf("coverage reasons = %v, missing %q", reasons, family)
		}
	}
}

func TestPiScanReportsTraversalFailures(t *testing.T) {
	plan := Scan(Roots{PiRoot: "synthetic\x00pi-root"})
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "Pi root cannot be read") {
		t.Fatalf("warnings = %v, want the Pi traversal failure", plan.Warnings)
	}
}
