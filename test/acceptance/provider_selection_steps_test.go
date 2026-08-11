//go:build acceptance

package acceptance

import (
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

func registerProviderSelectionSteps(ctx *godog.ScenarioContext, w *providerAcceptanceWorld) {
	ctx.When(`^I run Doctor$`, w.runDoctor)
	ctx.Then(`^Doctor names the titular model and explains the earlier failure$`, w.doctorNamesTitularModel)
	ctx.Then(`^the titular provider is "([^"]*)"$`, w.titularProvider)
	ctx.Then(`^"([^"]*)" is unavailable before the ready local floor$`, w.unavailableBeforeLocalFloor)
	ctx.Then(`^every configured provider is named with its failure reason$`, w.everyProviderHasFailureReason)
}

func (w *providerAcceptanceWorld) runDoctor() error { return w.run("doctor", "--json") }

func (w *providerAcceptanceWorld) doctorNamesTitularModel() error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	reported := objectList(doc["providers"])
	if len(reported) != len(w.providers) {
		return fmt.Errorf("Doctor reported %d providers, want %d", len(reported), len(w.providers))
	}
	if reported[0]["provider"] != w.providers[0].Name || reported[0]["ready"] != false ||
		strings.TrimSpace(fmt.Sprint(reported[0]["reason"])) == "" {
		return fmt.Errorf("Doctor did not explain the earlier provider failure: %v", reported[0])
	}
	titular := reported[len(reported)-1]
	fixture := w.providers[len(w.providers)-1]
	if titular["provider"] != fixture.Name || titular["model"] != fixture.Model || titular["ready"] != true {
		return fmt.Errorf("titular report = %v, want ready %s/%s", titular, fixture.Name, fixture.Model)
	}
	if doc["titular_provider"] != fixture.Name {
		return fmt.Errorf("titular_provider = %v, want %s", doc["titular_provider"], fixture.Name)
	}
	return nil
}

func (w *providerAcceptanceWorld) titularProvider(want string) error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	if got := fmt.Sprint(doc["titular_provider"]); got != want {
		return fmt.Errorf("titular provider %q, want %q", got, want)
	}
	return nil
}

func (w *providerAcceptanceWorld) unavailableBeforeLocalFloor(frontier string) error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	providers := objectList(doc["providers"])
	if len(providers) < 2 || providers[0]["provider"] != frontier || providers[0]["ready"] != false ||
		providers[1]["provider"] != "ollama" || providers[1]["ready"] != true {
		return fmt.Errorf("selection did not fall from %s to ready ollama: %v", frontier, providers)
	}
	return nil
}

func (w *providerAcceptanceWorld) everyProviderHasFailureReason() error {
	doc, err := w.lastJSON()
	if err != nil {
		return err
	}
	providers := objectList(doc["providers"])
	if len(providers) < len(w.providers) {
		return fmt.Errorf("tried %d providers, want at least the %d configured providers", len(providers), len(w.providers))
	}
	for i, provider := range providers[:len(w.providers)] {
		if provider["provider"] != w.providers[i].Name || provider["ready"] != false ||
			strings.TrimSpace(fmt.Sprint(provider["reason"])) == "" {
			return fmt.Errorf("provider failure %d is incomplete: %v", i, provider)
		}
	}
	for i, provider := range providers[len(w.providers):] {
		if provider["ready"] != false || strings.TrimSpace(fmt.Sprint(provider["reason"])) == "" {
			return fmt.Errorf("additional factory diagnostic %d is incomplete: %v", i, provider)
		}
	}
	return nil
}
