package cli

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/distribution/lifecycle"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/skill"
	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

const legacyCredentialsDir = "credentials"

var legacyProviderCredentialFiles = map[string]string{
	provider.NameCodex: "codex.json",
	"deepseek":         "deepseek.key",
	"zai":              "zai.key",
	"xai":              "xai.key",
}

// uninstallCommand leaves the machine as it was.
//
// Interactive by default, with `--keep-data` and `--purge` for scripts. The
// default answer keeps the
// data: a question whose Enter key deletes a corpus is a trap, and the operator
// who wants it gone types `n` and gets it.
func uninstallCommand(env *cliEnv) *cobra.Command {
	var keepData, purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove La Roca, and with your consent its data too",
		Long: "Unlinks the binary, withdraws La Roca's entry from every agent\n" +
			"configuration it was declared in, and asks whether to keep the database.\n\n" +
			"The purge converges over the state it finds: it runs on a machine a\n" +
			"previous attempt left halfway, it can be run twice, and it deletes nothing\n" +
			"La Roca did not create. Whatever it refuses to delete is named, with the\n" +
			"reason, so the operator can finish the job themselves.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if keepData && purge {
				return fmt.Errorf("--keep-data and --purge ask for opposite things")
			}
			wipe := purge
			if !keepData && !purge {
				answer, err := env.askAboutTheData(cmd.InOrStdin())
				if err != nil {
					return err
				}
				wipe = answer
			}
			return env.uninstall(cmd, wipe)
		},
	}
	cmd.Flags().BoolVar(&keepData, "keep-data", false, "remove the binary and keep the database")
	cmd.Flags().BoolVar(&purge, "purge", false, "remove the binary and the data")
	return cmd
}

// askAboutTheData is the question from the operator's own flow, worded the way
// the operator sees it. `n` purges; Enter keeps, because the destructive answer may not
// be the one a distracted operator gives by reflex.
func (env *cliEnv) askAboutTheData(in io.Reader) (bool, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return false, err
	}
	fmt.Fprintf(env.errOut, "Keep the Roca database and data at %s? [Y/n]: ",
		dirOf(paths.DB))

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		// Nobody answered. Silence is read as the answer that can be taken
		// back, which is the one that keeps the data.
		fmt.Fprintln(env.errOut, "no answer: your data stays where it is")
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(line), "n"), nil
}

// uninstall reverts the integrations and applies the removal plan.
//
// The integrations go first, and on purpose: they name the binary, so an agent
// left pointing at a file that is gone is the one residue an operator does not
// find until their next session fails to start.
func (env *cliEnv) uninstall(cmd *cobra.Command, purge bool) error {
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	report := lifecycle.Report{Purged: true, Deleted: []string{}}
	runtimes := env.withdrawTheIntegrations(&report, purge)

	// A binary that cannot say where it is is one this command leaves alone: the
	// rest of the uninstall still happens and the operator deletes one file.
	running, _ := os.Executable()
	plan := lifecycle.Plan{Binary: running}
	var applied lifecycle.Report
	if purge {
		// The execution is recorded before the operator-authorized purge removes
		// the log directory itself. Execute then suppresses its ordinary post-run
		// record so uninstall leaves the promised zero residue.
		if !env.started.IsZero() {
			env.capture(map[string]any{"purge_requested": true})
			// The trace never fails the command, and least of all this one: the
			// operator authorized a purge. A log that could not be written is
			// named in the report and the purge goes on. prelogged is set either
			// way, so the ordinary post-run record cannot recreate the log
			// directory the purge is about to remove.
			if err := env.logExecution(cmd, env.started, ExitOK, nil); err != nil {
				failed(&report, "record this run before the purge: %v", err)
			}
			env.prelogged = true
		}
		dataDir := dirOf(paths.DB)
		applied = applyPurge(dataDir, func() lifecycle.Plan {
			return lifecycle.Plan{Binary: running, Owned: ownedPaths(paths), DataDir: dataDir}
		})
	} else {
		applied = plan.Apply()
	}
	report.Deleted = append(report.Deleted, applied.Deleted...)
	report.Kept = append(report.Kept, applied.Kept...)
	report.Errors = append(report.Errors, applied.Errors...)
	report.Purged = report.Purged && applied.Purged

	if !report.Purged {
		env.code = ExitError
	}
	if env.json {
		kept := withoutDBKept(report.Kept, paths.DB)
		return env.printJSON(map[string]any{
			"purged": purge && report.Purged,
			// With no daemon there is no process to stop: every command opens the
			// database, works and exits.
			"stopped":    true,
			"deleted":    withoutDBPaths(report.Deleted, paths.DB),
			"kept":       kept,
			"kept_count": len(kept),
			"runtimes":   runtimes,
			"errors":     scrubDBPaths(report.Errors, paths.DB),
		})
	}
	renderUninstall(env, purge, report, runtimes)
	return nil
}

func applyPurge(dataDir string, plan func() lifecycle.Plan) lifecycle.Report {
	report := lifecycle.Report{Purged: true, Deleted: []string{}}
	logs := logfile.New(dataDir)
	if info, err := os.Lstat(filepath.Dir(logs.LockPath())); err == nil && !info.IsDir() {
		return plan().Apply()
	}
	release, err := logs.Lock()
	if err != nil {
		failed(&report, "lock product logs for purge: %v", err)
		return report
	}
	report = plan().Apply()
	if err := release(); err != nil {
		failed(&report, "release the product log lock after purge: %v", err)
	}
	final := lifecycle.Plan{
		Owned:   []string{logs.LockPath(), filepath.Dir(logs.LockPath())},
		DataDir: dataDir,
	}.Apply()
	return reconcilePurge(report, final, logs.LockPath())
}

func reconcilePurge(report, final lifecycle.Report, lockPath string) lifecycle.Report {
	report.Deleted = append(report.Deleted, final.Deleted...)
	report.Kept = append(report.Kept, final.Kept...)
	report.Errors = append(report.Errors, final.Errors...)
	if _, err := os.Lstat(lockPath); os.IsNotExist(err) {
		remaining := report.Errors[:0]
		prefix := "delete " + lockPath + ":"
		for _, failure := range report.Errors {
			if !strings.HasPrefix(failure, prefix) {
				remaining = append(remaining, failure)
			}
		}
		report.Errors = remaining
	}
	kept := report.Kept[:0]
	seen := map[string]bool{}
	for _, survivor := range report.Kept {
		if _, err := os.Lstat(survivor.Path); (err == nil || !os.IsNotExist(err)) && !seen[survivor.Path] {
			kept = append(kept, survivor)
			seen[survivor.Path] = true
		}
	}
	report.Kept = kept
	report.Purged = len(report.Errors) == 0
	return report
}

// failed records an error and takes the verdict down with it. An error appended
// without touching Purged printed "purged: yes" directly under its own error
// lines, which is the divergence the readable report exists to prevent.
func failed(report *lifecycle.Report, format string, args ...any) {
	report.Errors = append(report.Errors, fmt.Sprintf(format, args...))
	report.Purged = false
}

// withdrawTheIntegrations takes La Roca's entry out of every runtime's own MCP
// configuration.
//
// A runtime whose file is not there is not an error and not a line of output: a
// machine where the operator never installed `hermes` is the normal case, and
// five lines saying so on every uninstall is noise.
func (env *cliEnv) withdrawTheIntegrations(report *lifecycle.Report, purge bool) []agentcfg.Outcome {
	var outcomes []agentcfg.Outcome
	// What changed is named, what failed is named, and what was left behind is
	// named.
	withdrawn := func(what string, outcome agentcfg.Outcome, err error) {
		if err != nil {
			failed(report, "withdraw %s: %v", what, err)
			return
		}
		if outcome.Changed {
			outcomes = append(outcomes, outcome)
			if !purge {
				keepTheBackup(report, outcome)
			}
		}
	}

	for _, runtime := range agentcfg.Runtimes() {
		path, err := configFileOf(runtime, "")
		if err != nil {
			failed(report, "%s", err)
			continue
		}
		outcome, err := agentcfg.Uninstall(runtime, path)
		withdrawn("roca from "+runtime, outcome, err)
		if purge {
			removeRecoveryBackups(report, path)
		}
	}

	// The Claude signing hook lives in a different file from Claude's MCP
	// declaration and names the binary this command is about to unlink, so a
	// hook left behind fires `roca` on every Bash tool call and finds nothing.
	if settings, err := claudeSettingsPath(); err != nil {
		failed(report, "%s", err)
	} else {
		outcome, warning, err := uninstallClaudeAuthorshipHook(settings)
		if warning != "" {
			fmt.Fprintln(env.errOut, warning)
		}
		withdrawn("the Claude signing hook from "+settings, outcome, err)
		if purge {
			removeRecoveryBackups(report, settings)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		failed(report, "home: %v", err)
		return outcomes
	}
	for _, runtime := range skill.Runtimes() {
		path, err := skill.Path(runtime, home, os.Getenv)
		if err != nil {
			failed(report, "%s", err)
			continue
		}
		outcome, err := skill.Uninstall(runtime, path)
		if err != nil {
			failed(report, "withdraw skill from %s: %v", runtime, err)
			continue
		}
		if outcome.Changed {
			report.Deleted = append(report.Deleted, outcome.Removed...)
		}
	}
	return outcomes
}

// keepTheBackup names every recovery copy left beside a configuration file the
// withdrawal touched, and removes the one the withdrawal itself just made.
//
// The withdrawal's own copy holds the file with La Roca's entry still in it —
// the exact state being removed — so it is taken back. A regular uninstall
// keeps earlier recovery copies and names them. Purge takes the other branch
// and deletes the whole recovery family under the operator's consent.
func keepTheBackup(report *lifecycle.Report, outcome agentcfg.Outcome) {
	if outcome.Backup != "" {
		os.Remove(outcome.Backup)
	}
	nameSurvivingBackups(report, outcome.Path)
}

// removeRecoveryBackups applies the purge consent to the recovery copies La
// Roca created beside an agent configuration. A regular uninstall keeps and
// names them; --purge removes the whole product-owned family, including copies
// left by a previous interrupted withdrawal.
func removeRecoveryBackups(report *lifecycle.Report, configFile string) {
	for _, path := range recoveryBackupsFor(report, configFile) {
		if err := os.Remove(path); err != nil {
			failed(report, "delete %s: %v", path, err)
			continue
		}
		report.Deleted = append(report.Deleted, path)
	}
}

// nameSurvivingBackups reports every `config.bak*` beside configFile as a kept
// survivor, so an operator reads what the withdrawal left behind. The live
// config file is excluded: it is named on the "withdrawn from" line.
func nameSurvivingBackups(report *lifecycle.Report, configFile string) {
	for _, path := range recoveryBackupsFor(report, configFile) {
		report.Kept = append(report.Kept, lifecycle.Kept{
			Path:   path,
			Reason: "La Roca's recovery backup of your configuration: delete it yourself if you no longer need it",
		})
	}
}

func recoveryBackupsFor(report *lifecycle.Report, configFile string) []string {
	paths, err := recoveryBackups(configFile)
	if err != nil {
		failed(report, "%s", err)
	}
	return paths
}

func recoveryBackups(configFile string) ([]string, error) {
	dir := filepath.Dir(configFile)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read recovery backups beside %s: %w", configFile, err)
	}
	base := filepath.Base(configFile) + ".roca.bak"
	var paths []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !recoveryBackupName(name, base) {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	return paths, nil
}

func recoveryBackupName(name, base string) bool {
	if name == base {
		return true
	}
	suffix := strings.TrimPrefix(name, base+".")
	if suffix == name || suffix == "" {
		return false
	}
	_, err := strconv.ParseUint(suffix, 10, 64)
	return err == nil
}

// ownedPaths is the declaration of exact product paths and product file-name
// families. Directories are included only so lifecycle can remove them empty.
//
// The journals are named explicitly because SQLite writes them beside the
// database and a WAL left behind is a file with the operator's data in it.
func ownedPaths(paths config.Paths) []string {
	dataDir := dirOf(paths.DB)
	owned := []string{
		paths.DB, paths.DB + "-wal", paths.DB + "-shm", paths.DB + "-journal",
		paths.Config, paths.Reconciliation, filepath.Join(dataDir, "prompt.md"),
	}
	backupPrefix := strings.TrimSuffix(filepath.Base(paths.DB), ".db") + "."
	backups, backupsExist := ownedFiles(paths.Backups, func(name string) bool {
		if !strings.HasPrefix(name, backupPrefix) || !strings.HasSuffix(name, ".backup.db") {
			return false
		}
		stamp := strings.TrimSuffix(strings.TrimPrefix(name, backupPrefix), ".backup.db")
		_, err := time.Parse("20060102T150405Z", stamp)
		return err == nil
	})
	owned = append(owned, backups...)
	if backupsExist {
		owned = append(owned, paths.Backups)
	}
	cacheDir := filepath.Join(dataDir, "cache")
	if realDirectory(cacheDir) {
		owned = append(owned, filepath.Join(cacheDir, modelsDevCacheFile), cacheDir)
	}
	credentialsDir := filepath.Join(dataDir, legacyCredentialsDir)
	if realDirectory(credentialsDir) {
		credentialPaths := legacyProviderCredentialPaths(dataDir)
		for _, name := range slices.Sorted(maps.Keys(credentialPaths)) {
			owned = append(owned, credentialPaths[name])
		}
		owned = append(owned, credentialsDir)
	}
	logDir := filepath.Join(dataDir, logfile.DirName)
	logs, logsExist := ownedFiles(logDir, ownedLogName)
	owned = append(owned, logs...)
	if logsExist {
		owned = append(owned, logfile.New(dataDir).LockPath(), logDir)
	}
	return owned
}

func legacyProviderCredentialPaths(dataDir string) map[string]string {
	directory := filepath.Join(dataDir, legacyCredentialsDir)
	paths := make(map[string]string, len(legacyProviderCredentialFiles))
	for name, file := range legacyProviderCredentialFiles {
		paths[name] = filepath.Join(directory, file)
	}
	return paths
}

func ownedFiles(dir string, owns func(string) bool) ([]string, bool) {
	if !realDirectory(dir) {
		return nil, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, true
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() && owns(entry.Name()) {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	return paths, true
}

func realDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

func ownedLogName(name string) bool {
	for _, stream := range []string{logfile.Executions, logfile.MCPAudit, logfile.Ingest} {
		prefix := stream + "-"
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		stamp := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".jsonl")
		if _, err := time.Parse(time.DateOnly, stamp); err == nil {
			return true
		}
	}
	return false
}

// scrubDBPaths takes the database's location out of error PROSE. withoutDBPaths
// filters entries that are the path exactly, which is what the deleted list
// holds; an error is a sentence with the path inside it, so filtering left it
// published. The suffixes are replaced before the bare path, or `roca.db-wal`
// would come out as `the database-wal`.
func scrubDBPaths(messages []string, dbPath string) []string {
	if dbPath == "" {
		return messages
	}
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		for _, suffix := range []string{"-wal", "-shm", "-journal", ""} {
			message = strings.ReplaceAll(message, dbPath+suffix, "the database"+suffix)
		}
		out = append(out, message)
	}
	return out
}

// withoutDBPaths filters out paths that reveal the database file from a string
// slice. It matches the database path and its WAL/SHM/journal siblings exactly,
// so that the JSON surface never exposes where the database lives.
func withoutDBPaths(paths []string, dbPath string) []string {
	if dbPath == "" {
		return paths
	}
	out := make([]string, 0, len(paths))
	vault := map[string]bool{
		dbPath: true, dbPath + "-wal": true,
		dbPath + "-shm": true, dbPath + "-journal": true,
	}
	for _, p := range paths {
		if !vault[p] {
			out = append(out, p)
		}
	}
	return out
}

// withoutDBKept filters a Kept list the same way withoutDBPaths filters strings.
func withoutDBKept(kept []lifecycle.Kept, dbPath string) []lifecycle.Kept {
	if dbPath == "" {
		return kept
	}
	out := make([]lifecycle.Kept, 0, len(kept))
	vault := map[string]bool{
		dbPath: true, dbPath + "-wal": true,
		dbPath + "-shm": true, dbPath + "-journal": true,
	}
	for _, k := range kept {
		if !vault[k.Path] {
			out = append(out, k)
		}
	}
	return out
}

func renderUninstall(env *cliEnv, purge bool, report lifecycle.Report,
	runtimes []agentcfg.Outcome) {

	for _, outcome := range runtimes {
		env.print("withdrawn from %s: %s", outcome.Runtime, outcome.Path)
	}
	for _, path := range report.Deleted {
		env.print("deleted: %s", path)
	}
	if purge {
		env.print("kept paths: %d", len(report.Kept))
	}
	for _, kept := range report.Kept {
		env.print("kept: %s (%s)", kept.Path, kept.Reason)
	}
	for _, failure := range report.Errors {
		env.print("error: %s", failure)
	}
	// The verdict is the OUTCOME and never the request. --json reports
	// `purge && report.Purged`, and a readable line that branched on `purge`
	// alone printed "purged: yes" directly under the errors that say it did not.
	switch {
	case purge && report.Purged:
		env.print("purged: yes")
	case purge:
		env.print("purged: no (what is left is named above: run the uninstall again)")
	default:
		env.print("purged: no (your data is still where it was)")
	}
}
