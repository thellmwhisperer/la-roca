//go:build acceptance

package acceptance

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/creack/pty"
	"github.com/cucumber/godog"
)

func registerProviderCredentialSteps(ctx *godog.ScenarioContext, w *providerAcceptanceWorld) {
	ctx.Given(`^a pre-existing "(API key|OAuth|stale credential)" provider configuration$`, w.legacyProviderConfiguration)
	ctx.Given(`^a fake Claude Code binary is available$`, w.fakeClaudeBinary)
	ctx.When(`^I query through the legacy provider configuration$`, w.queryLegacyConfiguration)
	ctx.When(`^I inspect the model report$`, w.inspectModelReport)
	ctx.Then(`^the retired provider remains usable$`, w.retiredProviderRemainsUsable)
	ctx.Then(`^its only open proposal offers to remove the retired credential file$`, w.onlyCredentialCleanupIsProposed)
	ctx.When(`^I (accept|decline) the first-run migration proposal$`, w.answerMigrationProposal)
	ctx.When(`^I inspect model check and Doctor help$`, w.inspectAuthenticationHelp)
	ctx.When(`^I check model "([^\"]*)"$`, w.verifyLocalCLI)
	ctx.Then(`^the legacy query output names the retired provider and why no model answered$`, w.legacyQueryIsHonest)
	ctx.Then(`^the legacy provider is migrated to "([^\"]*)"$`, w.legacyProviderMigrated)
	ctx.Then(`^the legacy provider configuration is unchanged$`, w.legacyProviderUnchanged)
	ctx.Then(`^both help surfaces say models authenticate through their own CLIs$`, w.helpExplainsExternalAuthentication)
	ctx.Then(`^neither help surface advertises a stored model credential$`, w.helpHasNoStoredCredentialFlow)
	ctx.Then(`^the output says La Roca stores no secrets$`, w.outputSaysNoSecrets)
	ctx.Then(`^the output says configuration was not changed$`, w.outputSaysConfigurationUnchanged)
	ctx.Then(`^no model credential directory exists$`, w.noModelCredentialDirectory)
	ctx.Then(`^the output contains no traceback$`, w.providerOutputHasNoTraceback)
}

func (w *providerAcceptanceWorld) legacyProviderConfiguration(kind string) error {
	w.legacyProvider = "xai"
	table := "[models.xai]\nbase_url = \"https://example.invalid/v1\"\napi_key = \"legacy-acceptance-secret\"\nmodel = \"grok-legacy\"\n"
	credentialFile := "xai.key"
	// Short budgets measure the decision, not the patience. The one kind that
	// has to reach a real local CLI is given the time to start one.
	budgets := "timeout_ms = 250\nprobe_ms = 100\n"
	switch kind {
	case "OAuth":
		w.legacyProvider = "codex"
		table = "[models.codex]\nbase_url = \"https://synthetic.invalid/backend-api/codex\"\nmodel = \"gpt-legacy\"\n"
		credentialFile = "codex.json"
	case "stale credential":
		// Nothing retired is configured: the only leftover is the file an
		// older release wrote, and a shipped CLI still serves this provider.
		w.legacyProvider = "codex"
		table = "[models.codex]\nmodel = \"gpt-legacy\"\n"
		credentialFile = "codex.json"
		budgets = "timeout_ms = 5000\nprobe_ms = 5000\n"
	}
	legacyDir := filepath.Join(w.home, ".roca", "credentials")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(legacyDir, credentialFile), []byte("legacy-acceptance-secret"), 0o600); err != nil {
		return err
	}
	w.legacyConfig = "# operator note\n[models]\norder = [\"" + w.legacyProvider + "\", \"ollama\"]\n" + budgets + "\n" + table + "\n[models.ollama]\nbase_url = \"" + providerDeadEndpoint + "\"\nmodel = \"local-acceptance\"\n"
	return w.writeConfig(w.legacyConfig)
}

func (w *providerAcceptanceWorld) queryLegacyConfiguration() error {
	return w.run("query", "a question requiring a model", "--json")
}

func (w *providerAcceptanceWorld) legacyQueryIsHonest() error {
	all := w.last.stdout + w.last.stderr
	for _, want := range []string{w.legacyProvider, "ignored", "no model"} {
		if !strings.Contains(strings.ToLower(all), strings.ToLower(want)) {
			return fmt.Errorf("legacy degradation omitted %q: %s", want, all)
		}
	}
	return nil
}

func (w *providerAcceptanceWorld) inspectModelReport() error {
	return w.run("doctor", "--json")
}

func (w *providerAcceptanceWorld) retiredProviderRemainsUsable() error {
	document, err := w.lastJSON()
	if err != nil {
		return err
	}
	for _, entry := range objectList(document["providers"]) {
		if name, _ := entry["provider"].(string); name != w.legacyProvider {
			continue
		}
		if ready, _ := entry["ready"].(bool); !ready {
			return fmt.Errorf("%s is not usable: %v", w.legacyProvider, entry["reason"])
		}
		return nil
	}
	return fmt.Errorf("a leftover credential file removed %s from the cascade: %s",
		w.legacyProvider, w.last.stdout)
}

func (w *providerAcceptanceWorld) onlyCredentialCleanupIsProposed() error {
	document, err := w.lastJSON()
	if err != nil {
		return err
	}
	proposals, _ := document["capability_proposals"].([]any)
	var about []string
	for _, raw := range proposals {
		alert, _ := raw.(string)
		if strings.Contains(alert, w.legacyProvider) {
			about = append(about, alert)
		}
	}
	if len(about) != 1 || !strings.Contains(about[0],
		"Retired provider credential file detected for "+w.legacyProvider) {
		return fmt.Errorf("proposals about %s = %v, want only the credential cleanup",
			w.legacyProvider, about)
	}
	return nil
}

func (w *providerAcceptanceWorld) answerMigrationProposal(answer string) error {
	input := "n\nn\n"
	if answer == "accept" {
		input = "y\nn\n"
	}
	return w.runDoctorTTY(input)
}

func (w *providerAcceptanceWorld) runDoctorTTY(input string) error {
	command := exec.Command(w.binary, "doctor")
	command.Env = w.commandEnvironment()
	terminal, err := pty.Start(command)
	if err != nil {
		return err
	}
	if _, err := terminal.Write([]byte(input)); err != nil {
		_ = terminal.Close()
		return err
	}
	raw, readErr := io.ReadAll(terminal)
	_ = terminal.Close()
	waitErr := command.Wait()
	w.last = run{command: "roca doctor", stdout: string(raw)}
	if exit, ok := waitErr.(*exec.ExitError); ok {
		w.last.code = exit.ExitCode()
	} else if waitErr != nil {
		return waitErr
	}
	if readErr != nil && !strings.Contains(strings.ToLower(readErr.Error()), "input/output error") {
		return readErr
	}
	return nil
}

func (w *providerAcceptanceWorld) legacyProviderMigrated(target string) error {
	raw, err := os.ReadFile(w.configPath())
	if err != nil {
		return err
	}
	text := string(raw)
	if !strings.Contains(text, "# operator note") || !strings.Contains(text, `order = ["`+target) {
		return fmt.Errorf("migration did not preserve the document or select %s:\n%s", target, text)
	}
	for _, retired := range []string{"legacy-acceptance-secret", "base_url = \"https://synthetic.invalid", "[models.xai]"} {
		if strings.Contains(text, retired) {
			return fmt.Errorf("retired provider setting %q survived:\n%s", retired, text)
		}
	}
	credentialFile := "xai.key"
	if w.legacyProvider == "codex" {
		credentialFile = "codex.json"
	}
	credential := filepath.Join(w.home, ".roca", "credentials", credentialFile)
	if _, err := os.Stat(credential); !os.IsNotExist(err) {
		return fmt.Errorf("retired credential survived at %s: %v", credential, err)
	}
	if w.legacyProvider != "codex" {
		backup, err := os.ReadFile(w.configPath() + ".roca.bak")
		if err != nil {
			return err
		}
		if strings.Contains(string(backup), "legacy-acceptance-secret") {
			return fmt.Errorf("provider secret survived in recovery backup: %s", backup)
		}
	}
	return nil
}

func (w *providerAcceptanceWorld) legacyProviderUnchanged() error {
	raw, err := os.ReadFile(w.configPath())
	if err != nil {
		return err
	}
	if string(raw) != w.legacyConfig {
		return fmt.Errorf("declined migration changed config:\n--- want ---\n%s--- got ---\n%s", w.legacyConfig, raw)
	}
	credentialFile := "xai.key"
	if w.legacyProvider == "codex" {
		credentialFile = "codex.json"
	}
	if _, err := os.Stat(filepath.Join(w.home, ".roca", "credentials", credentialFile)); err != nil {
		return fmt.Errorf("declined migration removed legacy credential: %v", err)
	}
	return nil
}

func (w *providerAcceptanceWorld) inspectAuthenticationHelp() error {
	w.statements = nil
	for _, args := range [][]string{{"model", "check", "--help"}, {"doctor", "--help"}} {
		if err := w.run(args...); err != nil {
			return err
		}
		w.statements = append(w.statements, w.last)
	}
	return nil
}

func (w *providerAcceptanceWorld) authenticationHelp() string {
	var joined strings.Builder
	for _, statement := range w.statements {
		joined.WriteString(statement.stdout)
		joined.WriteString(statement.stderr)
	}
	return joined.String()
}

func (w *providerAcceptanceWorld) helpExplainsExternalAuthentication() error {
	all := strings.ToLower(w.authenticationHelp())
	if strings.Count(all, "through their own cli") < 2 || strings.Count(all, "stores no secrets") < 2 {
		return fmt.Errorf("help does not explain the authentication boundary twice: %s", all)
	}
	return nil
}

func (w *providerAcceptanceWorld) helpHasNoStoredCredentialFlow() error {
	all := strings.ToLower(w.authenticationHelp())
	for _, retired := range []string{"api key", "oauth", "credentials directory", "roca logout"} {
		if strings.Contains(all, retired) {
			return fmt.Errorf("help still advertises %q: %s", retired, all)
		}
	}
	return nil
}

func (w *providerAcceptanceWorld) fakeClaudeBinary() error {
	path, err := w.writeFakeBinary("claude", `printf '%s' '{"result":"SELECT 1"}'`)
	if err != nil {
		return err
	}
	w.environment["PATH"] = filepath.Dir(path)
	return nil
}

func (w *providerAcceptanceWorld) verifyLocalCLI(name string) error {
	return w.run("model", "check", name)
}

func (w *providerAcceptanceWorld) outputSaysNoSecrets() error {
	if !strings.Contains(strings.ToLower(w.last.stdout+w.last.stderr), "stores no secrets") {
		return fmt.Errorf("output omitted zero-secret boundary: %s%s", w.last.stdout, w.last.stderr)
	}
	return nil
}

func (w *providerAcceptanceWorld) outputSaysConfigurationUnchanged() error {
	if !strings.Contains(strings.ToLower(w.last.stdout+w.last.stderr), "configuration was not changed") {
		return fmt.Errorf("output omitted the no-write result: %s%s", w.last.stdout, w.last.stderr)
	}
	return nil
}

func (w *providerAcceptanceWorld) noModelCredentialDirectory() error {
	_, err := os.Stat(filepath.Join(w.home, ".roca", "credentials"))
	if !os.IsNotExist(err) {
		return fmt.Errorf("model credential directory exists: %v", err)
	}
	return nil
}

func (w *providerAcceptanceWorld) providerOutputHasNoTraceback() error {
	if strings.Contains(strings.ToLower(w.last.stdout+w.last.stderr), "panic:") {
		return fmt.Errorf("traceback in output: %s%s", w.last.stdout, w.last.stderr)
	}
	return nil
}
