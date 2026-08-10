package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/distribution/lifecycle"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/skill"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

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
	fmt.Fprintf(env.out, "Keep the Roca database and data at %s? [Y/n]: ",
		dirOf(paths.DB))

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		// Nobody answered. Silence is read as the answer that can be taken
		// back, which is the one that keeps the data.
		env.print("no answer: your data stays where it is")
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
	if purge {
		// The execution is recorded before the operator-authorized purge removes
		// the log directory itself. Execute then suppresses its ordinary post-run
		// record so uninstall leaves the promised zero residue.
		if !env.started.IsZero() {
			env.capture(map[string]any{"purge_requested": true})
			if err := env.logExecution(cmd, env.started, ExitOK, nil); err != nil {
				return err
			}
			env.prelogged = true
		}
		plan.Owned = ownedPaths(paths)
		plan.DataDir = dirOf(paths.DB)
	}
	applied := plan.Apply()
	report.Deleted = append(report.Deleted, applied.Deleted...)
	report.Kept = append(report.Kept, applied.Kept...)
	report.Errors = append(report.Errors, applied.Errors...)
	report.Purged = report.Purged && applied.Purged

	if !report.Purged {
		env.code = ExitError
	}
	if env.json {
		return env.printJSON(map[string]any{
			"purged": purge && report.Purged,
			// With no daemon there is no process to stop: every command opens the
			// database, works and exits.
			"stopped":  true,
			"deleted":  withoutDBPaths(report.Deleted, paths.DB),
			"kept":     withoutDBKept(report.Kept, paths.DB),
			"runtimes": runtimes,
			"errors":   withoutDBPaths(report.Errors, paths.DB),
		})
	}
	renderUninstall(env, purge, report, runtimes)
	return nil
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
			report.Errors = append(report.Errors,
				fmt.Sprintf("withdraw %s: %v", what, err))
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
			report.Errors = append(report.Errors, err.Error())
			continue
		}
		outcome, err := agentcfg.Uninstall(runtime, path)
		withdrawn("roca from "+runtime, outcome, err)
		if purge {
			removeRecoveryBackups(report, path)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("home: %v", err))
		return outcomes
	}
	for _, runtime := range skill.Runtimes() {
		path, err := skill.Path(runtime, home, os.Getenv)
		if err != nil {
			report.Errors = append(report.Errors, err.Error())
			continue
		}
		outcome, err := skill.Uninstall(runtime, path)
		if err != nil {
			report.Errors = append(report.Errors,
				fmt.Sprintf("withdraw skill from %s: %v", runtime, err))
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
			report.Errors = append(report.Errors, fmt.Sprintf("delete %s: %v", path, err))
			report.Purged = false
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
		report.Errors = append(report.Errors, err.Error())
		report.Purged = false
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
	base := filepath.Base(configFile)
	var paths []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, base+".bak") {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	return paths, nil
}

// ownedPaths is the declaration: every path this product creates in an
// installation, listed once. It is a declaration, not a filesystem walk.
//
// The journals are named explicitly because SQLite writes them beside the
// database and a WAL left behind is a file with the operator's data in it.
//
// The cache is declared at its ROOT and not only at this database's keyed
// subdirectory. Roca creates both, and declaring only the deepest one left a
// `cache/` behind on every machine that had ever trained a classifier: the
// directory survived, kept the whole data directory alive, and was then
// misreported as foreign.
func ownedPaths(paths config.Paths) []string {
	dataDir := dirOf(paths.DB)
	owned := []string{
		paths.DB, paths.DB + "-wal", paths.DB + "-shm", paths.DB + "-journal",
		paths.Config, paths.Backups, paths.CacheRoot, paths.Credentials,
		filepath.Join(dataDir, "prompt.md"), filepath.Join(dataDir, logfile.DirName),
	}
	// Skill files live under each runtime's own directory, outside ~/.roca.
	for _, runtime := range skill.Runtimes() {
		path, err := skill.Path(runtime, paths.Home, os.Getenv)
		if err != nil {
			continue
		}
		owned = append(owned, filepath.Dir(path))
	}
	return owned
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
	for _, kept := range report.Kept {
		env.print("kept: %s (%s)", kept.Path, kept.Reason)
	}
	for _, failure := range report.Errors {
		env.print("error: %s", failure)
	}
	if purge {
		env.print("purged: yes")
		return
	}
	env.print("purged: no (your data is still where it was)")
}
