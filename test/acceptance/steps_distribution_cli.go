//go:build acceptance

package acceptance

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"github.com/thellmwhisperer/la-roca/test/testfixture"
)

func registerDistributionCLISteps(ctx *godog.ScenarioContext, w *distributionWorld) {
	ctx.When(`^the operator asks for command-line help$`, w.askForCLIHelp)
	ctx.Then(`^every public command appears once with an honest one-line summary$`, w.helpIsComplete)
	ctx.When(`^the operator exercises the "([^"]*)" command in human and JSON form$`, w.exerciseOutputForms)
	ctx.Then(`^the default answer is human-readable and the requested answer is one JSON document$`, w.outputFormsAreExplicit)
	ctx.When(`^the operator runs an unknown command$`, w.runUnknownCommand)
	ctx.Then(`^it fails, names the unknown command, and points to help$`, w.unknownCommandIsHelpful)
	ctx.When(`^the operator exercises the plugin dispatch contract$`, w.exercisePluginDispatch)
	ctx.Then(`^arguments, standard input, output, and exit status cross the plugin seam untouched$`, w.pluginForwardsProcessContract)
	ctx.Then(`^built-ins win, missing plugins explain the convention, and plugins lists the fixtures$`, w.pluginBoundariesAreHonest)
	ctx.When(`^the operator tries to install a plugin without enabling experimental plugins$`, w.tryDisabledPluginInstall)
	ctx.Then(`^the installer is inert and names the feature flag$`, w.disabledPluginInstallerIsInert)
	ctx.Then(`^init reports setup, ingest, index, model, and its total once in that order$`, w.initSummaryIsOrdered)
	ctx.When(`^the operator initializes non-interactively with a detected model CLI$`, w.initWithDetectedModelCLI)
	ctx.Then(`^init prints one answering notice and writes no model configuration$`, w.initHasOneAnsweringNotice)
	ctx.When(`^the operator previews the default cron train with a gated plugin ride$`, w.previewDefaultCronTrain)
	ctx.Then(`^core ingest appears before the plugin ride and its dependency gate is closed$`,
		w.cronPreviewIsOrderedAndGated)
	ctx.Then(`^dry-run executes no ride and records no journey$`, w.cronDryRunIsInert)
}

func (w *distributionWorld) previewDefaultCronTrain() error {
	if err := w.prepare("cron-preview"); err != nil {
		return err
	}
	if err := writeFixture(filepath.Join(w.home, ".roca", "config.toml"),
		"[features]\ncron = true\n"); err != nil {
		return err
	}
	installed := w.runAt(w.home, w.installed, "_install-bundled-plugins", "--json")
	if installed.code != 0 {
		return fmt.Errorf("install bundled plugins: %+v", installed)
	}
	manifest := "[ride.vector_delta]\ncommand = \"roca vector ingest --delta\"\ngate = \"after_ingest\"\n"
	if err := testfixture.InstallRidePlugin(
		filepath.Join(w.home, ".roca", "plugins"), "vector-rides", manifest); err != nil {
		return err
	}
	w.state["cron-list"] = w.runAt(w.home, w.installed, "cron", "list")
	w.last = w.runAt(w.home, w.installed, "cron", "run", "--dry-run")
	database := filepath.Join(w.home, ".roca", "plugins", "roca-cron", "roca-cron.db")
	db, err := sql.Open("sqlite", database)
	if err != nil {
		return err
	}
	defer db.Close()
	var journeys int
	if err := db.QueryRow("SELECT count(*) FROM journeys").Scan(&journeys); err != nil {
		return err
	}
	w.state["cron-journeys"] = journeys
	return nil
}

func (w *distributionWorld) cronPreviewIsOrderedAndGated() error {
	listed, ok := w.state["cron-list"].(distributionRun)
	if !ok || listed.code != 0 {
		return fmt.Errorf("cron list = %+v", w.state["cron-list"])
	}
	core := strings.Index(listed.stdout, "core\tingest\tnightly")
	vector := strings.Index(listed.stdout, "vector-rides\tvector_delta\tnightly\tafter_ingest")
	if core < 0 || vector <= core {
		return fmt.Errorf("cron list is not ordered core then plugin:\n%s", listed.stdout)
	}
	if w.last.code != 0 || !strings.Contains(w.last.stdout, "core\tingest\tready") ||
		!strings.Contains(w.last.stdout, "vector-rides\tvector_delta\tdeferred_after_ingest") {
		return fmt.Errorf("cron preview = %+v", w.last)
	}
	return nil
}

func (w *distributionWorld) cronDryRunIsInert() error {
	if journeys, ok := w.state["cron-journeys"].(int); !ok || journeys != 0 {
		return fmt.Errorf("dry-run journey count = %#v", w.state["cron-journeys"])
	}
	return nil
}

func (w *distributionWorld) tryDisabledPluginInstall() error {
	if err := w.prepare("disabled-plugin-install"); err != nil {
		return err
	}
	w.last = w.runAtInput(w.home, w.installed, "yes\n", nil,
		"plugin", "install", filepath.Join(w.home, "source-that-does-not-exist"))
	return nil
}

func (w *distributionWorld) disabledPluginInstallerIsInert() error {
	if w.last.code == 0 || !strings.Contains(w.last.stderr, "features.plugins") {
		return fmt.Errorf("disabled plugin install = %+v", w.last)
	}
	entries, err := os.ReadDir(filepath.Join(w.home, ".roca", "plugins"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "roca-ops" && entry.Name() != "roca-corpus" &&
			entry.Name() != "roca-cron" && entry.Name() != "vector" {
			return fmt.Errorf("disabled plugin installer added %q", entry.Name())
		}
	}
	return nil
}

func (w *distributionWorld) initSummaryIsOrdered() error {
	if w.human.code != 0 {
		return fmt.Errorf("human init exited %d: %s%s", w.human.code, w.human.stdout, w.human.stderr)
	}
	position := -1
	for _, prefix := range []string{"setup:", "ingest:", "index:", "model:", "total:"} {
		if count := countDistributionLines(w.human.stdout, prefix); count != 1 {
			return fmt.Errorf("init %s line count=%d:\n%s", prefix, count, w.human.stdout)
		}
		next := strings.Index(w.human.stdout, "\n"+prefix)
		if strings.HasPrefix(w.human.stdout, prefix) {
			next = 0
		}
		if next <= position {
			return fmt.Errorf("init summary is not ordered at %s:\n%s", prefix, w.human.stdout)
		}
		position = next
	}
	return nil
}

func countDistributionLines(output, prefix string) int {
	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

func (w *distributionWorld) initWithDetectedModelCLI() error {
	if err := w.prepare("init-model-notice"); err != nil {
		return err
	}
	bin := filepath.Join(w.home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		return err
	}
	claude := filepath.Join(bin, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\nprintf '{\"result\":\"SELECT 1\"}\\n'\n"), 0o700); err != nil {
		return err
	}
	w.last = w.runAtInput(w.home, w.installed, "", []string{
		"PATH=" + bin, "ROCA_MODELS_ORDER=claude",
	}, "init", "--db-path", filepath.Join(w.home, ".roca", "roca.db"))
	return nil
}

func (w *distributionWorld) initHasOneAnsweringNotice() error {
	if w.last.code != 0 {
		return fmt.Errorf("non-interactive init exited %d: %s%s", w.last.code, w.last.stdout, w.last.stderr)
	}
	if count := countDistributionLines(w.last.stdout, "answering:"); count != 1 {
		return fmt.Errorf("answering line count=%d, want 1:\n%s", count, w.last.stdout)
	}
	for _, want := range []string{"answering: claude/sonnet", "configuration:", "roca model set <id>"} {
		if !strings.Contains(w.last.stdout, want) {
			return fmt.Errorf("answering notice does not contain %q:\n%s", want, w.last.stdout)
		}
	}
	if strings.Contains(w.last.stdout+w.last.stderr, "Which model") {
		return fmt.Errorf("non-interactive init opened the chooser: %s%s", w.last.stdout, w.last.stderr)
	}
	if _, err := os.Stat(filepath.Join(w.home, ".roca", "config.toml")); !os.IsNotExist(err) {
		return fmt.Errorf("non-interactive init wrote model configuration: %v", err)
	}
	return nil
}

func (w *distributionWorld) askForCLIHelp() error {
	w.last = w.runAt(w.root, w.binary, "--help")
	return nil
}

func (w *distributionWorld) helpIsComplete() error {
	if w.last.code != 0 {
		return fmt.Errorf("help exited %d: %s", w.last.code, w.last.stderr)
	}
	honest := map[string]string{
		"doctor": "configuration", "ingest": "source", "init": "database",
		"hooks": "authorship", "index": "map", "layers": "layer", "model": "model", "query": "memory",
		"explore": "concept", "store": "memory",
		"uninstall": "remove", "update": "release", "plugin": "plugin", "plugins": "plugin",
	}
	found := map[string]string{}
	inCommands := false
	for _, line := range strings.Split(w.last.stdout, "\n") {
		switch strings.TrimSpace(line) {
		case "Available Commands:":
			inCommands = true
			continue
		case "Flags:":
			inCommands = false
		}
		if !inCommands || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return fmt.Errorf("command help is not one summary line: %q", line)
		}
		name := fields[0]
		if _, duplicate := found[name]; duplicate {
			return fmt.Errorf("command %q appears more than once", name)
		}
		found[name] = strings.Join(fields[1:], " ")
	}
	if len(found) != len(honest) {
		return fmt.Errorf("help lists %v, want exactly %v", mapKeys(found), mapKeys(honest))
	}
	for command, truth := range honest {
		summary, ok := found[command]
		if !ok || !strings.Contains(strings.ToLower(summary), truth) {
			return fmt.Errorf("%s summary %q does not explain %s", command, summary, truth)
		}
	}
	return nil
}

func (w *distributionWorld) exerciseOutputForms(command string) error {
	var channel *httptest.Server
	if command == "update" {
		channel = httptest.NewTLSServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
			out.Header().Set("Content-Type", "application/json")
			fmt.Fprint(out, `{"tag_name":"v99.0.0","assets":[]}`)
		}))
		defer channel.Close()
	}

	runOne := func(label string, machine bool) (distributionRun, error) {
		if err := w.prepare(label); err != nil {
			return distributionRun{}, err
		}
		if channel != nil {
			writeTLSCertificate(filepath.Join(w.home, "tls-ca.pem"), channel)
		}
		args, err := distributionCommandArgs(command, w.home, channel)
		if err != nil {
			return distributionRun{}, err
		}
		if machine {
			args = append(args, "--json")
		}
		if command == "plugins" {
			return w.runAtInput(w.home, w.installed, "", []string{"PATH=" + pluginFixtures(w.root)}, args...), nil
		}
		return w.runAt(w.home, w.installed, args...), nil
	}

	var err error
	w.human, err = runOne(command+"-human", false)
	if err != nil {
		return err
	}
	w.machine, err = runOne(command+"-json", true)
	return err
}

func distributionCommandArgs(command, home string, channel *httptest.Server) ([]string, error) {
	switch command {
	case "init":
		return []string{"init", "--db-path", home + "/alternate/roca.db"}, nil
	case "query":
		return []string{"query", "count memories"}, nil
	case "store":
		return []string{"store", "--layer", "discovery", "--content", "distribution output marker", "--origin", "agent"}, nil
	case "ingest":
		return []string{"ingest"}, nil
	case "model":
		return []string{"model", "check"}, nil
	case "doctor":
		return []string{"doctor"}, nil
	case "update":
		return []string{"update", "--check", "--api", channel.URL, "--repo", "example/roca"}, nil
	case "uninstall":
		return []string{"uninstall", "--keep-data"}, nil
	case "plugins":
		return []string{"plugins"}, nil
	case "index":
		return []string{"index"}, nil
	default:
		return nil, fmt.Errorf("unknown acceptance command %q", command)
	}
}

func (w *distributionWorld) exercisePluginDispatch() error {
	path := pluginFixtures(w.root)
	env := []string{"PATH=" + path}
	w.state["plugin"] = w.runAtInput(w.root, w.binary, "synthetic input\n", env,
		"demo", "alpha", "two words")
	w.state["builtin"] = w.runAtInput(w.root, w.binary, "", env, "version")
	w.state["missing"] = w.runAtInput(w.root, w.binary, "", env, "absent")
	w.state["list"] = w.runAtInput(w.root, w.binary, "", env, "plugins")
	return nil
}

func pluginFixtures(root string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(root)), "testdata", "plugins")
}

func (w *distributionWorld) pluginForwardsProcessContract() error {
	run := w.state["plugin"].(distributionRun)
	if run.code != 23 {
		return fmt.Errorf("plugin exit=%d, want 23: %s", run.code, run.stderr)
	}
	for _, want := range []string{"args: <alpha> <two words>", "stdin: <synthetic input>"} {
		if !strings.Contains(run.stdout, want) {
			return fmt.Errorf("plugin output does not contain %q: %q", want, run.stdout)
		}
	}
	if run.stderr != "fixture stderr\n" {
		return fmt.Errorf("plugin stderr = %q", run.stderr)
	}
	return nil
}

func (w *distributionWorld) pluginBoundariesAreHonest() error {
	builtin := w.state["builtin"].(distributionRun)
	if builtin.code != 0 || strings.Contains(builtin.stdout+builtin.stderr, "fixture built-in collision") {
		return fmt.Errorf("same-named plugin intercepted built-in: %+v", builtin)
	}
	missing := w.state["missing"].(distributionRun)
	if missing.code == 0 || !strings.Contains(missing.stderr, "roca-absent") || !strings.Contains(missing.stderr, "PATH") {
		return fmt.Errorf("missing plugin error does not explain the convention: %+v", missing)
	}
	listed := w.state["list"].(distributionRun)
	for _, name := range []string{"demo", "version"} {
		want := name + "\t" + filepath.Join(pluginFixtures(w.root), "roca-"+name)
		if !strings.Contains(listed.stdout, want) {
			return fmt.Errorf("plugin list does not contain %q: %q", want, listed.stdout)
		}
	}
	return nil
}

func (w *distributionWorld) outputFormsAreExplicit() error {
	if w.human.code != 0 {
		return fmt.Errorf("human command exited %d: %s%s", w.human.code, w.human.stdout, w.human.stderr)
	}
	if w.machine.code != 0 {
		return fmt.Errorf("JSON command exited %d: %s%s", w.machine.code, w.machine.stdout, w.machine.stderr)
	}
	if strings.TrimSpace(w.human.stdout) == "" || json.Valid([]byte(w.human.stdout)) {
		return fmt.Errorf("default output is empty or machine-shaped: %q", w.human.stdout)
	}
	if !json.Valid([]byte(w.machine.stdout)) {
		return fmt.Errorf("requested JSON is not one document: %q", w.machine.stdout)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(w.machine.stdout), &document); err != nil || len(document) == 0 {
		return fmt.Errorf("requested JSON has no object fields: %v", err)
	}
	return nil
}

func (w *distributionWorld) runUnknownCommand() error {
	w.last = w.runAt(w.root, w.binary, "distribution-command-that-does-not-exist")
	return nil
}

func (w *distributionWorld) unknownCommandIsHelpful() error {
	output := strings.ToLower(w.last.stdout + w.last.stderr)
	if w.last.code == 0 {
		return fmt.Errorf("unknown command succeeded")
	}
	for _, required := range []string{"distribution-command-that-does-not-exist", "help"} {
		if !strings.Contains(output, required) {
			return fmt.Errorf("unknown-command failure does not contain %q: %s", required, output)
		}
	}
	return nil
}

func mapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
