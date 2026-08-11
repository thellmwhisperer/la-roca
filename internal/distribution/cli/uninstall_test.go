package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/lifecycle"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
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
func TestThePurgeDoesNotClaimRuntimeSkillDirectories(t *testing.T) {
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

	for _, path := range owned {
		if strings.Contains(path, "skills") && strings.Contains(path, "roca") {
			t.Errorf("the data inventory claims a runtime skill directory: %s", path)
		}
		if !strings.HasPrefix(path, home) {
			t.Errorf("the inventory reaches outside the resolved home:\n  %s\n  home is %s",
				path, home)
		}
	}
}

func TestPurgePreservesGenericSiblingDirectoryContents(t *testing.T) {
	home := t.TempDir()
	paths := resolvedIn(t, home)
	foreign := filepath.Join(paths.Backups, "operator.txt")
	writeFile(t, foreign, "mine")
	report := lifecycle.Plan{Owned: ownedPaths(paths), DataDir: dirOf(paths.DB)}.Apply()
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("purge deleted an operator file from a generic directory: %v", err)
	}
	if !report.Purged || len(report.Errors) != 0 {
		t.Fatalf("preserving operator content failed the purge: %+v", report)
	}
	for _, path := range []string{foreign, paths.Backups, dirOf(paths.DB)} {
		if !slices.ContainsFunc(report.Kept, func(kept lifecycle.Kept) bool { return kept.Path == path }) {
			t.Errorf("preserved path %s was not named: %+v", path, report.Kept)
		}
	}
}

func TestPurgeRemovesAnAuditCreatedAfterTheOwnershipSnapshot(t *testing.T) {
	home := t.TempDir()
	paths := resolvedIn(t, home)
	dataDir, writer, foreign := preparedLogFixture(t, paths)

	done := make(chan error, 1)
	report := applyPurge(dataDir, func() lifecycle.Plan {
		plan := lifecycle.Plan{Owned: ownedPaths(paths), DataDir: dataDir}
		go func() {
			done <- writer.AppendExisting(logfile.MCPAudit, logfile.MCPRecord{})
		}()
		return plan
	})

	if err := <-done; err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("late audit append error = %v, want missing lifecycle lock", err)
	}
	if matches, err := filepath.Glob(filepath.Join(dataDir, logfile.DirName, "mcp-audit-*.jsonl")); err != nil || len(matches) != 0 {
		t.Fatalf("late product audit survived: %v, err=%v", matches, err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign log was not preserved: %v", err)
	}
	requireSuccessfulPurge(t, report)
}

func TestPurgeReleasesAndRemovesItsLogLock(t *testing.T) {
	home := t.TempDir()
	paths := resolvedIn(t, home)
	dataDir := dirOf(paths.DB)

	for range 2 {
		purgeOwnedPaths(t, paths)
		if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
			t.Fatalf("data directory survived purge: %v", err)
		}
	}
}

func TestPurgeReconciliationClearsOnlyResolvedLockFailures(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "logs", ".roca.lock")
	stale := "delete " + lockPath + ": the product-owned artifact remains"
	genuine := "delete another product file: permission denied"

	recovered := reconcilePurge(
		lifecycle.Report{Purged: false, Errors: []string{stale}},
		lifecycle.Report{Purged: true}, lockPath,
	)
	if !recovered.Purged || len(recovered.Errors) != 0 {
		t.Fatalf("recovered report = %+v", recovered)
	}

	failed := reconcilePurge(
		lifecycle.Report{Purged: false, Errors: []string{stale, genuine}},
		lifecycle.Report{Purged: true}, lockPath,
	)
	if failed.Purged || !slices.Equal(failed.Errors, []string{genuine}) {
		t.Fatalf("failed report = %+v", failed)
	}
}

func TestPurgeReconciliationKeepsTheOriginalReasonForAPath(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "owned-log.jsonl")
	writeFile(t, path, "still here")
	lockPath := filepath.Join(directory, ".roca.lock")
	original := lifecycle.Kept{Path: path, Reason: "La Roca created it and could not delete it: run the uninstall again"}
	later := lifecycle.Kept{Path: path, Reason: "La Roca did not create it: delete it yourself if you want to"}

	report := reconcilePurge(
		lifecycle.Report{Purged: false, Kept: []lifecycle.Kept{original}, Errors: []string{"delete " + path + ": permission denied"}},
		lifecycle.Report{Purged: true, Kept: []lifecycle.Kept{later}}, lockPath,
	)
	if !slices.Equal(report.Kept, []lifecycle.Kept{original}) {
		t.Fatalf("kept outcomes = %+v, want original reason", report.Kept)
	}
}

func TestPurgeReportsForeignLogSurvivorsOnce(t *testing.T) {
	home := t.TempDir()
	paths := resolvedIn(t, home)
	_, _, foreign := preparedLogFixture(t, paths)

	report := purgeOwnedPaths(t, paths)
	counts := map[lifecycle.Kept]int{}
	for _, survivor := range report.Kept {
		counts[survivor]++
	}
	for survivor, count := range counts {
		if count != 1 {
			t.Errorf("kept survivor reported %d times: %+v", count, survivor)
		}
	}
	if !slices.ContainsFunc(report.Kept, func(kept lifecycle.Kept) bool { return kept.Path == foreign }) {
		t.Fatalf("foreign log was not reported: %+v", report.Kept)
	}
}

func TestPurgePreservesSymlinkedProductDirectoriesAndTargets(t *testing.T) {
	home := t.TempDir()
	paths := resolvedIn(t, home)
	dataDir := dirOf(paths.DB)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	directories := []struct {
		path string
		name string
	}{
		{paths.Backups, "roca.20260811T120000Z.backup.db"},
		{filepath.Join(dataDir, "cache"), modelsDevCacheFile},
		{paths.Credentials, "codex.json"},
		{filepath.Join(dataDir, logfile.DirName), "executions-2026-08-11.jsonl"},
	}
	for _, directory := range directories {
		target := t.TempDir()
		foreign := filepath.Join(target, directory.name)
		writeFile(t, foreign, "mine")
		if err := os.Symlink(target, directory.path); err != nil {
			t.Skipf("symlinks are unavailable: %v", err)
		}
	}

	purgeOwnedPaths(t, paths)
	for _, directory := range directories {
		if info, err := os.Lstat(directory.path); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("symlink %s was not preserved: info=%v err=%v", directory.path, info, err)
		}
		if _, err := os.Stat(filepath.Join(directory.path, directory.name)); err != nil {
			t.Errorf("foreign target file was not preserved: %v", err)
		}
	}
}

func TestRecoveryBackupOwnershipIsProductSpecific(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.toml")
	writeFile(t, configFile+".bak", "operator")
	writeFile(t, configFile+".roca.bak", "roca")
	writeFile(t, configFile+".roca.bak.2", "roca")
	paths, err := recoveryBackups(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("owned recovery backups = %v", paths)
	}
	for _, path := range paths {
		if path == configFile+".bak" {
			t.Fatal("operator backup was claimed by the purge")
		}
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

func preparedLogFixture(t *testing.T, paths config.Paths) (string, *logfile.Writer, string) {
	t.Helper()
	dataDir := dirOf(paths.DB)
	writer := logfile.New(dataDir)
	if err := writer.Prepare(); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dataDir, logfile.DirName, "operator.txt")
	writeFile(t, foreign, "mine")
	return dataDir, writer, foreign
}

func purgeOwnedPaths(t *testing.T, paths config.Paths) lifecycle.Report {
	t.Helper()
	dataDir := dirOf(paths.DB)
	report := applyPurge(dataDir, func() lifecycle.Plan {
		return lifecycle.Plan{Owned: ownedPaths(paths), DataDir: dataDir}
	})
	requireSuccessfulPurge(t, report)
	return report
}

func requireSuccessfulPurge(t *testing.T, report lifecycle.Report) {
	t.Helper()
	if !report.Purged || len(report.Errors) != 0 {
		t.Fatalf("purge report = %+v", report)
	}
}

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
			report: lifecycle.Report{Purged: true, Deleted: []string{}, Kept: []lifecycle.Kept{{Path: "/home/.roca/operator.txt", Reason: "La Roca did not create it"}}},
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
			if !strings.Contains(out.String(), "kept paths: "+strconv.Itoa(len(want.report.Kept))) {
				t.Errorf("kept count is missing from\n%s", out.String())
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
