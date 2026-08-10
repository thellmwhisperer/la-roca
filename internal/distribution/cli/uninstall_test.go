package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/lifecycle"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

// The inventory is a declaration, so what is missing from it is invisible until
// somebody purges a real installation and finds it still there.
//
// This is that check, and it is written against the layout `config.Resolve`
// produces rather than a list copied by hand: a path this product creates and
// this list does not name is left behind AND named as somebody else's, which
// keeps the whole data directory alive with it.

// The cache root is Roca's own directory and is declared so the purge removes
// it rather than reporting it as someone else's, which would keep the data
// directory alive with it.
func TestThePurgeDeclaresTheCacheDirectoryItCreates(t *testing.T) {
	home := t.TempDir()
	paths := resolvedIn(t, home)

	// Whatever the product writes inside the cache is removed with its parent.
	if err := os.MkdirAll(paths.Cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Cache, "a-cached-artefact"),
		[]byte("something the product wrote"), 0o600); err != nil {
		t.Fatal(err)
	}

	data := dirOf(paths.DB)
	report := lifecycle.Plan{Owned: ownedPaths(paths), DataDir: data}.Apply()

	for _, kept := range report.Kept {
		t.Errorf("the purge kept %s: %s", kept.Path, kept.Reason)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		left, _ := os.ReadDir(data)
		var names []string
		for _, entry := range left {
			names = append(names, entry.Name())
		}
		t.Fatalf("the data directory survives the purge carrying %v", names)
	}
}

// The other half of the same rule, and the one that keeps the fix honest: what
// the operator left in the data directory is not La Roca's. A wider inventory
// may not turn into a wider deletion.
func TestAWiderInventoryStillDeletesNothingOfTheOperators(t *testing.T) {
	home := t.TempDir()
	paths := resolvedIn(t, home)

	data := dirOf(paths.DB)
	theirs := filepath.Join(data, "notes-of-my-own.txt")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(theirs, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	report := lifecycle.Plan{Owned: ownedPaths(paths), DataDir: data}.Apply()
	if _, err := os.Stat(theirs); err != nil {
		t.Fatalf("the purge deleted a file the operator put there: %v", err)
	}
	var named bool
	for _, kept := range report.Kept {
		named = named || kept.Path == theirs
	}
	if !named {
		t.Errorf("the purge left %s behind without naming it: %+v", theirs, report.Kept)
	}
}

// Skill files live outside ~/.roca, under each runtime's own directory. The
// purge inventory has to name them, or uninstall leaves them behind and the
// surviving skill still instructs agents to call a binary that is gone.
func TestThePurgeDeclaresSkillPaths(t *testing.T) {
	home := t.TempDir()
	paths := resolvedIn(t, home)
	owned := ownedPaths(paths)

	var found int
	for _, path := range owned {
		if strings.Contains(path, "skills") && strings.Contains(path, "roca") {
			found++
		}
	}
	if found < 5 {
		t.Errorf("owned paths include %d skill directories, want at least 5", found)
	}
}

// The full circle: install the skill and the integrations over a real
// installation, withdraw them, and read what is left. Two leftovers the acta
// found are asserted away here: the empty `skills/` directory the install
// created and nobody removed, and every recovery backup that survives the
// withdrawal (whether the purge may delete its own `.bak` files is a held
// decision; whatever survives is named, or the build fails).
func TestUninstallCleansTheEmptySkillChainAndNamesEverySurvivor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_DB_PATH", "")
	t.Setenv("ROCA_CONFIG", "")
	t.Setenv("ROCA_MODELS_ORDER", "none")
	// In production the binary is `roca`, and the install writes that name into
	// the runtime configuration. In-process the executable is the test binary,
	// so name a `roca` for the install to write.
	t.Setenv("ROCA_BIN", filepath.Join(home, "roca"))
	for _, key := range []string{
		"CLAUDE_CONFIG_DIR", "CODEX_HOME", "OPENCODE_CONFIG",
		"HERMES_HOME", "PI_CODING_AGENT_DIR",
	} {
		t.Setenv(key, "")
	}

	// Pre-existing operator configuration: install leaves recovery .bak files
	// beside these, and the uninstall must name every one that survives.
	claudeJSON := filepath.Join(home, ".claude.json")
	writeFile(t, claudeJSON, `{"model":"opus"}`)

	build := Build{Version: "test", Commit: "test-sha"}
	runRoot(t, build, "init", "--db-path", filepath.Join(home, ".roca", "roca.db"))
	runRoot(t, build, "skill", "install", "claude")
	runRoot(t, build, "mcp", "install", "claude")

	out := runRoot(t, build, "uninstall", "--purge")

	// The empty skills directory chain the install created is gone.
	skillsDir := filepath.Join(home, ".claude", "skills")
	if _, err := os.Stat(skillsDir); !os.IsNotExist(err) {
		left, _ := os.ReadDir(skillsDir)
		t.Fatalf("the empty skills directory survived carrying %d entries", len(left))
	}

	// Every file that survives under HOME is named in the narration.
	var unnamed []string
	_ = filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.Contains(out, path) {
			unnamed = append(unnamed, path)
		}
		return nil
	})
	if len(unnamed) > 0 {
		t.Errorf("the uninstall left surviving files unnamed:\n  %s\noutput:\n%s",
			strings.Join(unnamed, "\n  "), out)
	}
}

// --- helpers ---

// writeFile creates the parent directory and writes a file, the way an
// operator's pre-existing configuration looks before La Roca touches it.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// resolvedIn is the layout of one installation, asked of the package that owns
// it. Writing the paths out by hand here would measure this test's idea of the
// layout and not the product's.
func resolvedIn(t *testing.T, home string) config.Paths {
	t.Helper()
	paths, err := config.Resolve(config.Input{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(paths.Cache, dirOf(paths.DB)) {
		t.Fatalf("the cache %s does not hang off the data directory", paths.Cache)
	}
	return paths
}
