//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/cucumber/godog"
)

func registerDistributionCLISteps(ctx *godog.ScenarioContext, w *distributionWorld) {
	ctx.When(`^the operator asks for command-line help$`, w.askForCLIHelp)
	ctx.Then(`^every public command appears once with an honest one-line summary$`, w.helpIsComplete)
	ctx.When(`^the operator exercises the "([^"]*)" command in human and JSON form$`, w.exerciseOutputForms)
	ctx.Then(`^the default answer is human-readable and the requested answer is one JSON document$`, w.outputFormsAreExplicit)
	ctx.When(`^the operator runs an unknown command$`, w.runUnknownCommand)
	ctx.Then(`^it fails, names the unknown command, and points to help$`, w.unknownCommandIsHelpful)
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
		"login": "model", "query": "memory", "store": "memory",
		"uninstall": "remove", "update": "release",
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
		channel = httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
			out.Header().Set("Content-Type", "application/json")
			fmt.Fprint(out, `{"tag_name":"v99.0.0","assets":[]}`)
		}))
		defer channel.Close()
	}

	runOne := func(label string, machine bool) (distributionRun, error) {
		if err := w.prepare(label); err != nil {
			return distributionRun{}, err
		}
		args, err := distributionCommandArgs(command, w.home, channel)
		if err != nil {
			return distributionRun{}, err
		}
		if machine {
			args = append(args, "--json")
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
	case "login":
		return []string{"login"}, nil
	case "doctor":
		return []string{"doctor"}, nil
	case "update":
		return []string{"update", "--check", "--api", channel.URL, "--repo", "example/roca"}, nil
	case "uninstall":
		return []string{"uninstall", "--keep-data"}, nil
	default:
		return nil, fmt.Errorf("unknown acceptance command %q", command)
	}
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
