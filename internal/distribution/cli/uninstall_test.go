package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	// A runtime directory the operator declared in their environment would land
	// in the inventory too, and two tests here hand it straight to
	// lifecycle.Apply, which calls os.RemoveAll on every entry: on a machine with
	// CLAUDE_CONFIG_DIR set, running the suite deleted the operator's real skill
	// directory. resolvedIn isolates that, and this asserts the isolation holds.
	elsewhere := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(elsewhere, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(elsewhere, ".codex"))

	home := t.TempDir()
	paths := resolvedIn(t, home)
	owned := ownedPaths(paths)

	var found int
	for _, path := range owned {
		if strings.Contains(path, "skills") && strings.Contains(path, "roca") {
			found++
		}
		if !strings.HasPrefix(path, home) {
			t.Errorf("the inventory reaches outside the resolved home:\n  %s\n  home is %s",
				path, home)
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
	isolateRuntimeDirs(t, home)
	// In production the binary is `roca`, and the install writes that name into
	// the runtime configuration. In-process the executable is the test binary,
	// so name a `roca` for the install to write.
	t.Setenv("ROCA_BIN", filepath.Join(home, "roca"))

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

func TestUnreadableRecoveryBackupDirectoryFailsTheReport(t *testing.T) {
	notDirectory := filepath.Join(t.TempDir(), "agent-home")
	writeFile(t, notDirectory, "this path is a file")
	report := lifecycle.Report{Purged: true}

	if paths := recoveryBackupsFor(&report, filepath.Join(notDirectory, "config.toml")); len(paths) != 0 {
		t.Fatalf("an unreadable parent returned backup paths: %v", paths)
	}
	if report.Purged || len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "read recovery backups") {
		t.Fatalf("enumeration failure was not reported: %+v", report)
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

// isolateRuntimeDirs clears the runtime directory overrides before any path is
// resolved. The inventory is built with os.Getenv, and these tests hand it to
// lifecycle.Apply, which calls os.RemoveAll on every entry: an override left
// standing pointed the purge at the operator's real directories.
func isolateRuntimeDirs(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_DB_PATH", "")
	t.Setenv("ROCA_CONFIG", "")
	t.Setenv("ROCA_MODELS_ORDER", "none")
	for _, key := range []string{
		"CLAUDE_CONFIG_DIR", "CODEX_HOME", "OPENCODE_CONFIG",
		"HERMES_HOME", "PI_CODING_AGENT_DIR",
	} {
		t.Setenv(key, "")
	}
}

// resolvedIn is the layout of one installation, asked of the package that owns
// it. Writing the paths out by hand here would measure this test's idea of the
// layout and not the product's.
func resolvedIn(t *testing.T, home string) config.Paths {
	t.Helper()
	isolateRuntimeDirs(t, home)
	paths, err := config.Resolve(config.Input{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// The two surfaces of the same uninstall may not disagree about whether the
// purge worked. --json reports `purge && report.Purged`, so a purge that hit an
// error is false there; the readable line branched on the REQUEST instead of the
// OUTCOME and said "purged: yes" over the errors it had just printed.
func TestTheReadableUninstallReportsThePurgeOutcome(t *testing.T) {
	for _, want := range []struct {
		name   string
		report lifecycle.Report
		line   string
		absent string
	}{
		{
			name: "it failed",
			report: lifecycle.Report{Deleted: []string{},
				Errors: []string{"delete /home/.roca/roca.db: permission denied"}},
			line:   "purged: no",
			absent: "purged: yes",
		},
		{
			name:   "it worked",
			report: lifecycle.Report{Purged: true, Deleted: []string{}},
			line:   "purged: yes",
			absent: "purged: no",
		},
	} {
		t.Run(want.name, func(t *testing.T) {
			var out strings.Builder
			renderUninstall(&cliEnv{out: &out, errOut: &out}, true, want.report, nil)
			if !strings.Contains(out.String(), want.line) {
				t.Errorf("want %q in\n%s", want.line, out.String())
			}
			if strings.Contains(out.String(), want.absent) {
				t.Errorf("must not say %q in\n%s", want.absent, out.String())
			}
		})
	}
}

func TestAFailedWithdrawalIsNotReportedAsAPurge(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory cannot be made unwritable")
	}
	home := t.TempDir()
	isolateRuntimeDirs(t, home)

	// Codex declares roca inside a directory nothing may write, so withdrawing it
	// has to stage a file there and cannot. Every other runtime's file is absent,
	// which is not an error and not a line of output.
	codex := filepath.Join(home, ".codex")
	writeFile(t, filepath.Join(codex, "config.toml"),
		"[mcp_servers.roca]\ncommand = \"roca\"\nargs = [\"mcp\", \"serve\"]\n")
	if err := os.Chmod(codex, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(codex, 0o700) })

	var out strings.Builder
	env := &cliEnv{out: &out, errOut: &out, started: time.Now()}
	if err := env.uninstall(uninstallCommand(env), true); err != nil {
		t.Fatalf("uninstall returned an error instead of a report: %v", err)
	}

	if strings.Contains(out.String(), "purged: yes") {
		t.Errorf("a purge that could not withdraw reports itself as done:\n%s", out.String())
	}
	if env.code != ExitError {
		t.Errorf("exit code = %d, want %d", env.code, ExitError)
	}
}

// The JSON surface never says where the database lives. withoutDBPaths filtered
// entries that ARE the path exactly, which is right for the deleted list, but the
// error list carries prose with the path inside it, so an unlinking failure
// published the location the surface exists to withhold.
func TestTheJSONSurfaceScrubsTheDatabasePathOutOfErrors(t *testing.T) {
	const db = "/home/someone/.roca/roca.db"
	errors := []string{
		"delete " + db + "-wal: permission denied",
		"delete " + db + ": permission denied",
		"withdraw roca from codex: read-only file system",
	}

	for _, got := range scrubDBPaths(errors, db) {
		if strings.Contains(got, db) {
			t.Errorf("an error published the database path: %q", got)
		}
	}
	if len(scrubDBPaths(errors, db)) != len(errors) {
		t.Errorf("an error was dropped instead of scrubbed: %v", scrubDBPaths(errors, db))
	}
}
