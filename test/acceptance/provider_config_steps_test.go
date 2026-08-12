//go:build acceptance

package acceptance

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
)

func registerProviderConfigSteps(ctx *godog.ScenarioContext, w *providerAcceptanceWorld) {
	ctx.Given(`^an initialized home with no model$`, w.initializedHomeWithoutModel)
	ctx.Given(`^the provider configuration is:$`, w.providerConfiguration)
	ctx.Given(`^the configuration also contains the unknown key "([^"]*)"$`, w.addUnknownConfigKey)
	ctx.Given(`^the configuration file is missing$`, w.removeConfig)
	ctx.Given(`^a fake "([^"]+)" agent CLI binary is on PATH$`, w.fakeAgentCLIOnPath)
	ctx.Given(`^no configured provider is available$`, w.noProviderAvailable)
	ctx.Given(`^the model answers with SQL "([^"]*)"$`, w.modelAnswersSQL)
	ctx.Given(`^a provider backed by a local binary answers with SQL "([^"]*)"$`, w.localBinaryAnswersSQL)
	ctx.Then(`^the reported provider order is "([^"]*)"$`, w.reportedProviderOrder)
	ctx.Then(`^the output warns about "([^"]*)"$`, w.outputWarnsAbout)
	ctx.Then(`^the configuration is reported as absent$`, w.configReportedAbsent)
}

func (w *providerAcceptanceWorld) fakeAgentCLIOnPath(name string) error {
	path, err := w.writeFakeBinary(name, `printf '%s' 'SELECT 1'`)
	if err != nil {
		return err
	}
	w.environment["PATH"] = filepath.Dir(path) + string(os.PathListSeparator) + "/usr/bin:/bin"
	return nil
}

func (w *providerAcceptanceWorld) initializedHomeWithoutModel() error {
	if err := os.MkdirAll(filepath.Join(w.home, ".roca"), 0o700); err != nil {
		return err
	}
	if err := w.writeConfig("[models]\norder = []\n"); err != nil {
		return err
	}
	return w.mustRun("init", "--db-path", w.dbPath(), "--json")
}

func (w *providerAcceptanceWorld) providerConfiguration(table *godog.Table) error {
	fixtures, err := providerFixtures(table)
	if err != nil {
		return err
	}
	w.providers = fixtures
	return w.persistProviderConfiguration("")
}

func providerFixtures(table *godog.Table) ([]providerFixture, error) {
	if len(table.Rows) < 2 || len(table.Rows[0].Cells) != 3 {
		return nil, fmt.Errorf("provider table needs provider, model and availability columns")
	}
	var fixtures []providerFixture
	for _, row := range table.Rows[1:] {
		if len(row.Cells) != 3 {
			return nil, fmt.Errorf("provider row has %d cells, want 3", len(row.Cells))
		}
		fixtures = append(fixtures, providerFixture{
			Name: strings.TrimSpace(row.Cells[0].Value), Model: strings.TrimSpace(row.Cells[1].Value),
			Availability: strings.TrimSpace(row.Cells[2].Value),
		})
	}
	return fixtures, nil
}

func (w *providerAcceptanceWorld) persistProviderConfiguration(extra string) error {
	var body strings.Builder
	body.WriteString("[models]\norder = [")
	for i, fixture := range w.providers {
		if i > 0 {
			body.WriteString(", ")
		}
		body.WriteString(strconv.Quote(fixture.Name))
	}
	body.WriteString("]\ntimeout_ms = 250\nprobe_ms = 100\n")
	body.WriteString(extra)
	for i := range w.providers {
		fixture := &w.providers[i]
		fixture.BaseURL = providerDeadEndpoint
		if fixture.Availability == "ready" {
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/api/tags":
					out.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(out, `{"models":[{"name":%q,"model":%q}]}`, fixture.Model, fixture.Model)
				case "/api/chat":
					out.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(out, `{"message":{"content":%q}}`, w.modelSQL)
				default:
					http.NotFound(out, request)
				}
			}))
			w.readyServers = append(w.readyServers, server)
			fixture.BaseURL = server.URL
		}
		fmt.Fprintf(&body, "\n[models.%s]\nmodel = %s\n", fixture.Name, strconv.Quote(fixture.Model))
		if fixture.Name == "ollama" {
			fmt.Fprintf(&body, "base_url = %s\n", strconv.Quote(fixture.BaseURL))
		}
	}
	return w.writeConfig(body.String())
}

func (w *providerAcceptanceWorld) modelAnswersSQL(statement string) error {
	w.modelSQL = statement
	return nil
}

func (w *providerAcceptanceWorld) localBinaryAnswersSQL(statement string) error {
	path, err := w.writeFakeBinary("local-provider", `printf '%s' "$FAKE_PROVIDER_RESULT"`)
	if err != nil {
		return err
	}
	w.environment["FAKE_PROVIDER_RESULT"] = statement
	body := "[models]\norder = [\"local-binary\"]\n\n" +
		"[models.local-binary]\ncommand = [" + strconv.Quote(path) + "]\n" +
		"model = \"binary-acceptance\"\ntimeout_seconds = 2\n"
	return w.writeConfig(body)
}

func (w *providerAcceptanceWorld) writeFakeBinary(name, body string) (string, error) {
	dir := filepath.Join(w.home, "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		return "", err
	}
	return path, nil
}

func (w *providerAcceptanceWorld) addUnknownConfigKey(key string) error {
	parts := strings.Split(key, ".")
	if len(parts) != 2 || parts[0] != "models" {
		return fmt.Errorf("the fixture only supports an unknown models key, got %q", key)
	}
	return w.persistProviderConfiguration(parts[1] + " = 7\n")
}

func (w *providerAcceptanceWorld) removeConfig() error {
	w.environment["ROCA_OLLAMA_BASE_URL"] = providerDeadEndpoint
	w.environment["ROCA_CODEX_BASE_URL"] = providerDeadEndpoint
	err := os.Remove(w.configPath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (w *providerAcceptanceWorld) noProviderAvailable() error {
	w.providers = []providerFixture{
		{Name: "codex", Model: "frontier-acceptance", Availability: "unreachable"},
		{Name: "ollama", Model: "local-acceptance", Availability: "unreachable"},
	}
	return w.persistProviderConfiguration("")
}

func (w *providerAcceptanceWorld) reportedProviderOrder(want string) error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	providers := objectList(doc["providers"])
	names := make([]string, 0, len(providers))
	for _, item := range providers {
		names = append(names, fmt.Sprint(item["provider"]))
	}
	if got := strings.Join(names, ", "); got != want {
		return fmt.Errorf("provider order %q, want %q", got, want)
	}
	return nil
}

func (w *providerAcceptanceWorld) outputWarnsAbout(key string) error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	warnings, _ := doc["warnings"].([]any)
	for _, warning := range warnings {
		if strings.Contains(fmt.Sprint(warning), key) {
			return nil
		}
	}
	return fmt.Errorf("no warning names %q: %s", key, w.last.stdout)
}

func (w *providerAcceptanceWorld) configReportedAbsent() error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	if exists, ok := doc["config_exists"].(bool); !ok || exists {
		return fmt.Errorf("config_exists = %v, want false", doc["config_exists"])
	}
	return nil
}
