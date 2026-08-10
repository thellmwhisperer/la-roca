//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"strings"

	"github.com/cucumber/godog"
)

func registerProviderCredentialSteps(ctx *godog.ScenarioContext, w *providerAcceptanceWorld) {
	ctx.Given(`^the API key for "([^"]*)" has been stored through login$`, w.keyStoredThroughLogin)
	ctx.When(`^I log in to "([^"]*)" with the API key "([^"]*)"$`, w.loginWithAPIKey)
	ctx.When(`^I log out from "([^"]*)"$`, w.logout)
	ctx.Then(`^the credential is stored under the data directory at 0600$`, w.credentialStoredAt0600)
	ctx.Then(`^no output contains the API key$`, w.outputDoesNotContainKey)
	ctx.Then(`^the credential for "([^"]*)" is absent$`, w.credentialAbsent)
	ctx.Then(`^the output names the removed credential for "([^"]*)"$`, w.outputNamesRemovedCredential)
}

func (w *providerAcceptanceWorld) keyStoredThroughLogin(provider string) error {
	return w.loginWithAPIKey(provider, "provider-acceptance-secret")
}

func (w *providerAcceptanceWorld) loginWithAPIKey(provider, key string) error {
	w.credential = key
	return w.runWithInput(key+"\n", "login", provider)
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
