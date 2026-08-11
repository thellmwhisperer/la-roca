//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
)

func registerProviderCredentialSteps(ctx *godog.ScenarioContext, w *providerAcceptanceWorld) {
	ctx.Given(`^the API key for "([^"]*)" has been stored through login$`, w.keyStoredThroughLogin)
	ctx.Given(`^a fake Claude Code binary is available$`, w.fakeClaudeAvailable)
	ctx.When(`^I log in to "([^"]*)" with the API key "([^"]*)"$`, w.loginWithAPIKey)
	ctx.When(`^I log out from "([^"]*)"$`, w.logout)
	ctx.When(`^I log in to "([^"]*)" with model "([^"]*)"$`, w.loginWithModel)
	ctx.Then(`^the credential is stored under the data directory at 0600$`, w.credentialStoredAt0600)
	ctx.Then(`^no output contains the API key$`, w.outputDoesNotContainKey)
	ctx.Then(`^the credential for "([^"]*)" is absent$`, w.credentialAbsent)
	ctx.Then(`^the output names the removed credential for "([^"]*)"$`, w.outputNamesRemovedCredential)
	ctx.Then(`^the configuration chooses model "([^"]*)" for "([^"]*)"$`, w.configurationChoosesModel)
	ctx.Then(`^the output says La Roca never reads or stores the Claude credential$`, w.claudeCredentialIsExternal)
	ctx.Then(`^Doctor reports "([^"]*)" ready$`, w.doctorReportsReady)
}

func (w *providerAcceptanceWorld) fakeClaudeAvailable() error {
	path, err := w.writeFakeBinary("claude", `printf '%s' '{"result":"synthetic Claude answer"}'`)
	if err != nil {
		return err
	}
	w.environment["PATH"] = filepath.Dir(path) + string(os.PathListSeparator) + "/usr/bin:/bin"
	return nil
}

func (w *providerAcceptanceWorld) loginWithModel(provider, model string) error {
	return w.run("login", provider, "--model", model)
}

func (w *providerAcceptanceWorld) configurationChoosesModel(model, name string) error {
	raw, err := os.ReadFile(w.configPath())
	if err != nil {
		return err
	}
	want := "[models." + name + "]\nmodel = " + strconv.Quote(model)
	if !strings.Contains(string(raw), want) {
		return fmt.Errorf("configuration does not contain %q: %s", want, raw)
	}
	return nil
}

func (w *providerAcceptanceWorld) claudeCredentialIsExternal() error {
	all := w.last.stdout + w.last.stderr
	if !strings.Contains(all, "La Roca never reads or stores it") {
		return fmt.Errorf("Claude credential boundary is absent: %s", all)
	}
	return nil
}

func (w *providerAcceptanceWorld) doctorReportsReady(name string) error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	for _, item := range objectList(doc["providers"]) {
		if item["provider"] == name && item["ready"] == true {
			return nil
		}
	}
	return fmt.Errorf("Doctor did not report %s ready: %s", name, w.last.stdout)
}

func (w *providerAcceptanceWorld) keyStoredThroughLogin(provider string) error {
	return w.loginWithAPIKey(provider, "provider-acceptance-secret")
}

func (w *providerAcceptanceWorld) loginWithAPIKey(provider, key string) error {
	w.credential = key
	const model = "credential-acceptance-model"
	server := newOpenAIModelServer(model)
	w.readyServers = append(w.readyServers, server)
	if err := w.writeConfig(fmt.Sprintf("[models.%s]\nbase_url = %s\n", provider, strconv.Quote(server.URL))); err != nil {
		return err
	}
	return w.runWithInput(key+"\n", "login", provider, "--model", model)
}

func (w *providerAcceptanceWorld) logout(provider string) error {
	return w.run("logout", provider)
}

func (w *providerAcceptanceWorld) credentialStoredAt0600() error {
	path := w.credentialPath("xai")
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("credential is not under the data directory: %w", err)
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("credential mode = %o, want 600", info.Mode().Perm())
	}
	return nil
}

func (w *providerAcceptanceWorld) outputDoesNotContainKey() error {
	if w.credential == "" {
		return fmt.Errorf("the scenario has no API key to protect")
	}
	if strings.Contains(w.last.stdout+w.last.stderr, w.credential) {
		return fmt.Errorf("the API key leaked to output")
	}
	return nil
}

func (w *providerAcceptanceWorld) credentialAbsent(provider string) error {
	_, err := os.Stat(w.credentialPath(provider))
	if !os.IsNotExist(err) {
		return fmt.Errorf("credential still exists: %v", err)
	}
	return nil
}

func (w *providerAcceptanceWorld) outputNamesRemovedCredential(provider string) error {
	all := w.last.stdout + w.last.stderr
	if !strings.Contains(all, provider) || !strings.Contains(strings.ToLower(all), "credential") {
		return fmt.Errorf("logout does not name the removed credential for %s: %s", provider, all)
	}
	return nil
}
