//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

const providerDeadEndpoint = "http://127.0.0.1:1"

type providerAcceptanceWorld struct {
	binary         string
	home           string
	environment    map[string]string
	last           run
	statements     []run
	providers      []providerFixture
	legacyProvider string
	legacyConfig   string
	modelSQL       string
	pluginsEnabled bool
	readyServers   []*httptest.Server
}

type providerFixture struct {
	Name         string
	Model        string
	Availability string
	BaseURL      string
}

func registerProviderAcceptanceSteps(ctx *godog.ScenarioContext, binary string) {
	w := &providerAcceptanceWorld{binary: binary}

	ctx.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
		home, err := acceptanceTempDir("provider-acceptance-")
		if err != nil {
			return c, err
		}
		w.home = home
		w.environment = map[string]string{}
		w.last = run{}
		w.statements = nil
		w.providers = nil
		w.legacyProvider = ""
		w.legacyConfig = ""
		w.modelSQL = ""
		w.pluginsEnabled = false
		w.readyServers = nil
		return c, nil
	})
	ctx.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		for _, server := range w.readyServers {
			server.Close()
		}
		if w.home != "" {
			_ = os.RemoveAll(w.home)
		}
		return c, nil
	})

	registerProviderConfigSteps(ctx, w)
	registerProviderSelectionSteps(ctx, w)
	registerProviderQuerySteps(ctx, w)
	registerProviderCredentialSteps(ctx, w)
	ctx.Then(`^the command exits with code (\d+)$`, w.exitsWithCode)
}

func (w *providerAcceptanceWorld) exitsWithCode(expected int) error {
	if w.last.code != expected {
		return fmt.Errorf("exit code %d, want %d; stdout=%s stderr=%s", w.last.code, expected, w.last.stdout, w.last.stderr)
	}
	return nil
}

func (w *providerAcceptanceWorld) run(args ...string) error {
	return w.runWithInput("", args...)
}

func (w *providerAcceptanceWorld) mustRun(args ...string) error {
	if err := w.run(args...); err != nil {
		return err
	}
	if w.last.code != 0 {
		return fmt.Errorf("roca %s exited %d: %s", strings.Join(args, " "), w.last.code, w.last.stderr)
	}
	return nil
}

func (w *providerAcceptanceWorld) runWithInput(input string, args ...string) error {
	cmd := exec.Command(w.binary, args...)
	cmd.Env = w.commandEnvironment()
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	w.last = run{command: "roca " + strings.Join(args, " "), stdout: stdout.String(), stderr: stderr.String()}
	if err == nil {
		return nil
	}
	if exit, ok := err.(*exec.ExitError); ok {
		w.last.code = exit.ExitCode()
		return nil
	}
	return err
}

func (w *providerAcceptanceWorld) commandEnvironment() []string {
	tmp := filepath.Join(w.home, "tmp")
	_ = os.MkdirAll(tmp, 0o700)
	path := filepath.Join(w.home, "bin")
	if configured := w.environment["PATH"]; configured != "" {
		path = configured
	}
	environment := []string{
		"HOME=" + w.home,
		"PATH=" + path,
		"TMPDIR=" + tmp,
	}
	for key, value := range w.environment {
		if key == "PATH" {
			continue
		}
		environment = append(environment, key+"="+value)
	}
	return environment
}

func (w *providerAcceptanceWorld) lastJSON() (map[string]any, error) {
	var document map[string]any
	if err := json.Unmarshal([]byte(w.last.stdout), &document); err != nil {
		return nil, fmt.Errorf("%s did not return JSON: %w\n%s", w.last.command, err, w.last.stdout)
	}
	return document, nil
}

func (w *providerAcceptanceWorld) writeConfig(body string) error {
	if w.pluginsEnabled && !strings.Contains(body, "[features]") {
		body += "\n[features]\nplugins = true\n"
	}
	if err := os.MkdirAll(filepath.Dir(w.configPath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(w.configPath(), []byte(body), 0o600)
}

func (w *providerAcceptanceWorld) dbPath() string { return filepath.Join(w.home, ".roca", "roca.db") }

func (w *providerAcceptanceWorld) configPath() string {
	return filepath.Join(w.home, ".roca", "config.toml")
}

func objectList(value any) []map[string]any {
	raw, _ := value.([]any)
	objects := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if object, ok := item.(map[string]any); ok {
			objects = append(objects, object)
		}
	}
	return objects
}

func intValue(value any) int {
	number, _ := value.(float64)
	return int(number)
}
