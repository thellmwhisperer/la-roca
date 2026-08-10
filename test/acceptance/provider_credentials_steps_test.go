//go:build acceptance

/**
 * @overview Implements acceptance steps for provider login/logout and credential safety. ~125 lines, no public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at loginWithAPIKey            <- real catalogue/probe fixture
 *   2. registerProviderCredentialSteps     <- Gherkin binding
 *   3. Assertions verify mode, redaction, and deletion
 *
 *   MAIN FLOW
 *   Gherkin step -> fake live provider -> roca login/probe -> filesystem assertion
 *
 *   PUBLIC API
 *   ----------
 *   None (acceptance-tag test file)
 *
 *   INTERNALS
 *   ---------
 *   registerProviderCredentialSteps, loginWithAPIKey, credential assertions
 *
 * @exports
 * @deps godog, httptest; providerAcceptanceWorld
 */
package acceptance

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
)

// -- 1/2 CORE · registration and login/logout flows <- START HERE --

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

// -/ 1/2

// -- 2/2 HELPER · credential assertions --

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

// -/ 2/2
