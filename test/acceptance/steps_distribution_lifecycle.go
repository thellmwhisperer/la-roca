//go:build acceptance

package acceptance

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

func registerDistributionLifecycleSteps(ctx *godog.ScenarioContext, w *distributionWorld) {
	ctx.When(`^update checks an unreachable synthetic release endpoint$`, w.updateAgainstUnreachableRelease)
	ctx.Then(`^update fails plainly, the installation is unchanged and one audit record is added$`, w.failedUpdateChangesNothing)
	ctx.Given(`^two synthetic homes with every La Roca integration installed$`, w.twoFullyIntegratedHomes)
	ctx.When(`^one home uninstalls with data kept and the other consents to purge$`, w.uninstallAndPurge)
	ctx.Then(`^the first keeps only its data and the second has zero La Roca residue$`, w.lifecycleConsentIsRespected)
	ctx.When(`^the installer artefact catalogue is compared with the release code$`, w.compareArtefactNames)
	ctx.Then(`^every platform has one identical artefact name$`, w.artefactNamesAgree)
	ctx.Given(`^a row with text longer than the operator's budget$`, w.seedBudgetRow)
	ctx.When(`^the row is requested with a 48 character budget over "([^"]*)"$`, w.requestBudgetedRow)
	ctx.Then(`^no returned text field exceeds 48 characters$`, w.budgetIsRespected)
}

type lifecycleFixture struct {
	home         string
	binary       string
	operatorFile map[string]string
	configs      map[string]bool
	result       distributionRun
}

func (w *distributionWorld) twoFullyIntegratedHomes() error {
	keep, err := w.integratedHome("keep")
	if err != nil {
		return err
	}
	purge, err := w.integratedHome("purge")
	if err != nil {
		return err
	}
	w.state["keepFixture"], w.state["purgeFixture"] = keep, purge
	return nil
}

func (w *distributionWorld) integratedHome(label string) (*lifecycleFixture, error) {
	if err := w.prepare(label); err != nil {
		return nil, err
	}
	fixture := &lifecycleFixture{home: w.home, binary: w.installed, operatorFile: map[string]string{}, configs: map[string]bool{}}
	for _, runtime := range distributionAgents {
		path, _, err := configurationOf(runtime, w.home)
		if err != nil {
			return nil, err
		}
		config := distributionConfigContent(path)
		if err := writeAcceptanceFixture(path, config); err != nil {
			return nil, err
		}
		fixture.operatorFile[path] = config
		fixture.configs[path] = true
		instruction := filepath.Join(filepath.Dir(path), "OPERATOR-INSTRUCTIONS.md")
		if runtime == "claude" {
			instruction = filepath.Join(w.home, ".claude", "OPERATOR-INSTRUCTIONS.md")
		}
		content := "Operator-owned instructions for " + runtime + ".\n"
		if err := writeAcceptanceFixture(instruction, content); err != nil {
			return nil, err
		}
		fixture.operatorFile[instruction] = content
		if run := w.runAt(w.home, w.installed, "mcp", "install", runtime); run.code != 0 {
			return nil, fmt.Errorf("install %s MCP: %s", runtime, run.stderr)
		}
		if run := w.runAt(w.home, w.installed, "skill", "install", runtime); run.code != 0 {
			return nil, fmt.Errorf("install %s skill: %s", runtime, run.stderr)
		}
	}
	return fixture, nil
}

func distributionConfigContent(path string) string {
	switch filepath.Ext(path) {
	case ".toml":
		return "model = \"synthetic\"\n"
	case ".yaml":
		return "theme: dark\n"
	default:
		return "{\n  \"theme\": \"dark\"\n}\n"
	}
}

func (w *distributionWorld) uninstallAndPurge() error {
	keep := w.state["keepFixture"].(*lifecycleFixture)
	purge := w.state["purgeFixture"].(*lifecycleFixture)
	keep.result = w.runAt(keep.home, keep.binary, "uninstall", "--keep-data")
	purge.result = w.runAt(purge.home, purge.binary, "uninstall", "--purge")
	return nil
}

func (w *distributionWorld) lifecycleConsentIsRespected() error {
	keep := w.state["keepFixture"].(*lifecycleFixture)
	purge := w.state["purgeFixture"].(*lifecycleFixture)
	for name, fixture := range map[string]*lifecycleFixture{"keep": keep, "purge": purge} {
		if fixture.result.code != 0 {
			return fmt.Errorf("%s uninstall failed: %s%s", name, fixture.result.stdout, fixture.result.stderr)
		}
		if _, err := os.Stat(fixture.binary); !os.IsNotExist(err) {
			return fmt.Errorf("%s uninstall left the binary", name)
		}
		for path, expected := range fixture.operatorFile {
			current, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("%s changed operator file %s: %v", name, path, err)
			}
			if fixture.configs[path] {
				for _, line := range strings.Split(expected, "\n") {
					line = strings.TrimSpace(line)
					if line != "" && !strings.Contains(string(current), line) {
						return fmt.Errorf("%s configuration %s lost %q", name, path, line)
					}
				}
				if strings.Contains(strings.ToLower(string(current)), "roca") {
					return fmt.Errorf("%s configuration %s still declares La Roca", name, path)
				}
			} else if string(current) != expected {
				return fmt.Errorf("%s changed operator instruction file %s", name, path)
			}
		}
		for _, runtime := range distributionAgents {
			path, _ := distributionSkillPath(runtime, fixture.home)
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				return fmt.Errorf("%s left the %s skill", name, runtime)
			}
		}
	}
	for _, data := range []string{filepath.Join(keep.home, ".roca", "roca.db"), filepath.Join(keep.home, ".roca", "prompt.md")} {
		if _, err := os.Stat(data); err != nil {
			return fmt.Errorf("keep-data removed %s: %v", data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(purge.home, ".roca")); !os.IsNotExist(err) {
		return fmt.Errorf("purge left the data directory: %v", err)
	}
	var residue []string
	_ = filepath.WalkDir(purge.home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || purge.operatorFile[path] != "" {
			return nil
		}
		if strings.Contains(entry.Name(), ".bak") || entry.Name() == "roca" || strings.Contains(path, string(filepath.Separator)+"skills"+string(filepath.Separator)+"roca") {
			residue = append(residue, path)
		}
		return nil
	})
	if len(residue) > 0 {
		return fmt.Errorf("purge left La Roca residue: %v", residue)
	}
	return nil
}

func writeAcceptanceFixture(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(file, content)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
