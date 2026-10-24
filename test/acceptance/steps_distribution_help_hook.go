//go:build acceptance

package acceptance

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cucumber/godog"
)

const sessionAliasPrefix = "- Attach aliases and their tables: "

func registerDistributionHelpHookSteps(ctx *godog.ScenarioContext, w *distributionWorld) {
	ctx.Then(`^the command list has an "([^"]*)" line and a "([^"]*)" line, each with a description$`,
		w.helpListsCommands)
	ctx.Then(`^a vector call the companion does not take names how to turn vector search on$`,
		w.vectorWithoutCompanionNamesTheSwitch)
	ctx.When(`^the operator runs the codex session hook$`, func() error {
		return w.runCodexSessionHook("session-hook", false)
	})
	ctx.When(`^the operator runs the codex session hook without a database$`, func() error {
		return w.runCodexSessionHook("session-hook-no-database", true)
	})
	ctx.Then(`^the hook does not say an unqualified table returns 0 rows and says it is refused with the qualified candidates$`,
		w.hookSaysUnqualifiedIsRefused)
	ctx.Then(`^the hook carries one alias line naming plugin_roca_corpus with sessions and exchanges and plugin_roca_ops with memories$`,
		w.hookNamesBundledAliases)
	ctx.Then(`^every alias the hook names is attached to roca exec$`, w.hookAliasesAreAttached)
	ctx.Then(`^the hook carries the craft text and no alias line$`, w.hookCarriesOnlyTheCraft)
	ctx.When(`^the operator runs the codex session hook with first-receipts installed and features.plugins=false$`,
		w.runCodexSessionHookWithDisabledPlugin)
	ctx.Then(`^the hook names no alias of first-receipts$`, w.hookOmitsDisabledPlugin)
}

func (w *distributionWorld) helpListsCommands(first, second string) error {
	if w.last.code != 0 {
		return fmt.Errorf("help exited %d: %s", w.last.code, w.last.stderr)
	}
	for _, name := range []string{first, second} {
		line := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(name) + ` +\S.*$`)
		if !line.MatchString(w.last.stdout) {
			return fmt.Errorf("help has no %q line with a description:\n%s", name, w.last.stdout)
		}
	}
	return nil
}

func (w *distributionWorld) vectorWithoutCompanionNamesTheSwitch() error {
	run := w.runAt(w.root, w.binary, "vector", "query", "harbor")
	if run.code == 0 || !strings.Contains(run.stderr, "vector = true") ||
		!strings.Contains(run.stderr, "roca vector install") {
		return fmt.Errorf("vector without its companion = %+v", run)
	}
	return nil
}

func (w *distributionWorld) runCodexSessionHook(label string, withoutDatabase bool) error {
	if err := w.prepare(label); err != nil {
		return err
	}
	if withoutDatabase {
		if err := os.Remove(filepath.Join(w.home, ".roca", "plugins", "roca-ops", "roca-ops.db")); err != nil {
			return err
		}
	}
	run := w.runAt(w.home, w.installed, "hooks", "run", "session", "--runtime", "codex")
	var envelope struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if run.code != 0 || json.Unmarshal([]byte(run.stdout), &envelope) != nil {
		return fmt.Errorf("codex session hook = %+v", run)
	}
	w.state["hook-body"] = envelope.HookSpecificOutput.AdditionalContext
	return nil
}

func (w *distributionWorld) hookBody() string {
	body, _ := w.state["hook-body"].(string)
	return body
}

func (w *distributionWorld) hookSaysUnqualifiedIsRefused() error {
	body := w.hookBody()
	if strings.Contains(body, "returns 0 rows without a warning") ||
		!strings.Contains(body, "an unqualified table name is refused with the qualified candidates") {
		return fmt.Errorf("hook body:\n%s", body)
	}
	return nil
}

// hookAliasLine returns the alias line's aliases mapped to their tables.
func (w *distributionWorld) hookAliasLine() (map[string][]string, error) {
	var lines []string
	for _, line := range strings.Split(w.hookBody(), "\n") {
		if strings.HasPrefix(line, sessionAliasPrefix) {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		return nil, fmt.Errorf("hook carries %d alias lines:\n%s", len(lines), w.hookBody())
	}
	aliases := map[string][]string{}
	entry := regexp.MustCompile(`(\w+) \(([^)]*)\)`)
	for _, match := range entry.FindAllStringSubmatch(lines[0], -1) {
		aliases[match[1]] = strings.Split(match[2], ", ")
	}
	return aliases, nil
}

func (w *distributionWorld) hookNamesBundledAliases() error {
	aliases, err := w.hookAliasLine()
	if err != nil {
		return err
	}
	for alias, tables := range map[string][]string{
		"plugin_roca_corpus": {"sessions", "exchanges"},
		"plugin_roca_ops":    {"memories"},
	} {
		for _, table := range tables {
			if !strings.Contains(", "+strings.Join(aliases[alias], ", ")+",", ", "+table+",") {
				return fmt.Errorf("alias %s does not name %s: %v", alias, table, aliases)
			}
		}
	}
	return nil
}

func (w *distributionWorld) hookAliasesAreAttached() error {
	aliases, err := w.hookAliasLine()
	if err != nil {
		return err
	}
	for alias, tables := range aliases {
		run := w.runAt(w.home, w.installed, "exec", "SELECT count(*) FROM "+alias+"."+tables[0])
		if run.code != 0 {
			return fmt.Errorf("hook names %s, which roca exec does not attach: %+v", alias, run)
		}
	}
	return nil
}

func (w *distributionWorld) hookCarriesOnlyTheCraft() error {
	body := w.hookBody()
	if !strings.Contains(body, "## La Roca is this machine's agent memory") ||
		!strings.Contains(body, "an unqualified table name is refused with the qualified candidates") ||
		strings.Contains(body, sessionAliasPrefix) {
		return fmt.Errorf("hook body without a database:\n%s", body)
	}
	return nil
}

func (w *distributionWorld) runCodexSessionHookWithDisabledPlugin() error {
	if err := w.prepare("session-hook-disabled-plugin"); err != nil {
		return err
	}
	source := filepath.Join(w.root, "first-receipts")
	if err := os.MkdirAll(source, 0o700); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", filepath.Join(source, "receipts.db"))
	if err != nil {
		return err
	}
	_, execErr := db.Exec(`CREATE TABLE receipts (id INTEGER PRIMARY KEY, title TEXT, amount_cents INTEGER);
		INSERT INTO receipts (title, amount_cents) VALUES ('coffee', 420)`)
	closeErr := db.Close()
	if execErr != nil {
		return execErr
	}
	if closeErr != nil {
		return closeErr
	}
	manifest := `{
  "schema": 1, "name": "first-receipts", "version": "1.0.0", "binary": "roca",
  "databases": [{
    "name": "records", "path": "receipts.db", "alias": "first_receipts",
    "attachment": "on-demand", "retention": "Keep receipts until the operator removes them."
  }],
  "semantic": {"databases": [{
    "database": "records", "description": "Purchase receipts and their totals.",
    "questions": ["Which receipts were recorded?"],
    "tables": [{"name": "receipts", "description": "One row per purchase receipt.",
      "columns": ["id", "title", "amount_cents"]}]
  }]}
}`
	if err := writeFixture(filepath.Join(source, "plugin.json"), manifest); err != nil {
		return err
	}
	var checksums strings.Builder
	for _, name := range []string{"plugin.json", "receipts.db"} {
		body, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			return err
		}
		fmt.Fprintf(&checksums, "%x  %s\n", sha256.Sum256(body), name)
	}
	if err := writeFixture(filepath.Join(source, "checksums.txt"), checksums.String()); err != nil {
		return err
	}
	installed := w.runAt(w.home, w.installed, "plugin", "install", source, "--yes")
	if installed.code != 0 {
		return fmt.Errorf("install first-receipts: %+v", installed)
	}
	statement := "SELECT title, amount_cents FROM first_receipts.receipts"
	if run := w.runAt(w.home, w.installed, "exec", statement); run.code != 0 ||
		!strings.Contains(run.stdout, "coffee") {
		return fmt.Errorf("first-receipts with plugins enabled: %+v", run)
	}
	config := strings.Replace(distributionPreparedConfig, "plugins = true", "plugins = false", 1)
	if err := writeFixture(filepath.Join(w.home, ".roca", "config.toml"), config); err != nil {
		return err
	}
	if run := w.runAt(w.home, w.installed, "exec", statement); run.code == 0 {
		return fmt.Errorf("roca exec attached first-receipts with plugins disabled: %+v", run)
	}
	run := w.runAt(w.home, w.installed, "hooks", "run", "session", "--runtime", "codex")
	var envelope struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if run.code != 0 || json.Unmarshal([]byte(run.stdout), &envelope) != nil {
		return fmt.Errorf("codex session hook with plugins disabled: %+v", run)
	}
	w.state["hook-body"] = envelope.HookSpecificOutput.AdditionalContext
	return nil
}

func (w *distributionWorld) hookOmitsDisabledPlugin() error {
	aliases, err := w.hookAliasLine()
	if err != nil {
		return err
	}
	if _, found := aliases["first_receipts"]; found {
		return fmt.Errorf("hook names disabled first-receipts alias: %v", aliases)
	}
	return nil
}
