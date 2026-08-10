package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/lifecycle"
	"github.com/thellmwhisperer/la-roca/internal/skill"
)

// uninstallCommand leaves the machine as it was.
//
// Interactive by default because that is the flow the operator runs (PRD R5),
// and with `--keep-data` / `--purge` for scripts. The default answer keeps the
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
			return env.uninstall(wipe)
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
func (env *cliEnv) uninstall(purge bool) error {
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	report := lifecycle.Report{Purged: true, Deleted: []string{}}
	runtimes := env.withdrawTheIntegrations(&report)

	// A binary that cannot say where it is is one this command leaves alone: the
	// rest of the uninstall still happens and the operator deletes one file.
	running, _ := os.Executable()
	plan := lifecycle.Plan{Binary: running}
	if purge {
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
			// With no daemon there is no process to stop and none is left
			// behind: every command opens the database, works and exits
			// (TECH-SPEC 8.5, D-6 disappears by construction).
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
func (env *cliEnv) withdrawTheIntegrations(report *lifecycle.Report) []agentcfg.Outcome {
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
			keepTheBackup(report, outcome)
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
// the exact state being removed — so it is taken back. The copy the install
// made of the operator's original is a different file and a different question:
// whether the purge may delete La Roca's `.bak` files at all is a held
// decision, so whatever survives is named instead, and a
// survivor that is not named is exactly the leftover the acta found.
func keepTheBackup(report *lifecycle.Report, outcome agentcfg.Outcome) {
	if outcome.Backup != "" {
		os.Remove(outcome.Backup)
	}
	nameSurvivingBackups(report, outcome.Path)
}

// nameSurvivingBackups reports every `config.bak*` beside configFile as a kept
// survivor, so an operator reads what the withdrawal left behind. The live
// config file is excluded: it is named on the "withdrawn from" line.
func nameSurvivingBackups(report *lifecycle.Report, configFile string) {
	dir := filepath.Dir(configFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	base := filepath.Base(configFile)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == base || !strings.HasPrefix(name, base+".bak") {
			continue
		}
		report.Kept = append(report.Kept, lifecycle.Kept{
			Path:   filepath.Join(dir, name),
			Reason: "La Roca's recovery backup of your configuration: delete it yourself if you no longer need it",
		})
	}
}

// ownedPaths is the declaration: every path this product creates in an
// installation, listed once. It is a declaration and not a walk of the
// filesystem, which is the whole of the D-7 fix (internal/lifecycle).
//
// The journals are named explicitly because SQLite writes them beside the
// database and a WAL left behind is a file with the operator's data in it.
//
// The cache is declared at its ROOT and not only at this database's keyed
// subdirectory. Roca creates both, and declaring only the deepest one left a
// `cache/` behind on every machine that had ever trained a classifier: the
// directory survived, it kept the whole data directory alive with it, and the
// second half of D-7 then reported La Roca's own directory as a file La Roca
// did not create.
func ownedPaths(paths config.Paths) []string {
	dataDir := dirOf(paths.DB)
	owned := []string{
		paths.DB, paths.DB + "-wal", paths.DB + "-shm", paths.DB + "-journal",
		paths.Config, paths.Backups, paths.CacheRoot, paths.Credentials,
		filepath.Join(dataDir, "prompt.md"),
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
