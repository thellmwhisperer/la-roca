//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

// Issue 589: after the migration each memory is embedded once, by the sidecar
// that owns it. The home is the operator's: installed by install.sh, made by
// roca init, migrated, served in cutover, embedded with the pinned model.

func registerMemoryOwnershipSteps(ctx *godog.ScenarioContext, m *world) {
	ctx.Given(`^a home made by roca init with vector = true( and roca_ops = false)?, over an agent session that carries a memory mentioning "([^"]*)"$`,
		m.homeWithSessionMemory)
	ctx.Given(`^the operator ran roca store --layer discovery --content "([^"]*)" before the migration$`,
		func(content string) error {
			return m.mustRun(`roca store --layer discovery --content "` + content + `"`)
		})
	ctx.Given(`^roca migrate is verified and serving = "cutover" is set under layout$`, m.migratedToCutover)
	ctx.When(`^I vector-query "([^"]*)" in databases "([^"]*)"$`, func(phrase, databases string) error {
		_, err := m.runWith("roca vector query", []string{"vector", "query", phrase, "20",
			"--databases", databases, "--json"})
		return err
	})
	ctx.Then(`^the ops sidecar has (some|0) embedded chunks and a state other than "outdated"$`,
		m.opsSidecarHas)
	ctx.Then(`^the vector hits holding "([^"]*)" are one memory from "([^"]*)"$`, m.memoryHitsAreOne)
}

func (m *world) homeWithSessionMemory(opsOff, words string) error {
	cwd := filepath.Join(m.home, "workspace", "harbor")
	project := filepath.Join(m.home, ".claude", "projects", encodeAgentPath(cwd))
	if err := writeFixture(filepath.Join(project, seededSessionID+".jsonl"), fmt.Sprintf(
		"{\"type\":\"user\",\"timestamp\":\"2026-08-01T10:00:00Z\",\"cwd\":%q,\"message\":{\"content\":\"where did we leave the %s\"}}\n"+
			"{\"type\":\"assistant\",\"timestamp\":\"2026-08-01T10:00:02Z\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"on the pier\"}]}}\n",
		cwd, words)); err != nil {
		return err
	}
	if err := writeFixture(filepath.Join(project, "memory", "harbor.md"),
		"The "+words+" hangs on the north pier.\n"); err != nil {
		return err
	}
	// init keeps a config that is already there, so this home is made with vector on.
	config := "[features]\nvector = true\n"
	if opsOff != "" {
		config += "roca_ops = false\n"
	}
	if err := writeFixture(filepath.Join(m.home, ".roca", "config.toml"), config); err != nil {
		return err
	}
	if err := installFrozenVectorModel(m.home); err != nil {
		return err
	}
	if err := m.mustRun(m.initCommand(true)); err != nil {
		return err
	}
	// The companion's state folder, as the frozen labs make it (issue 571).
	return prepareFrozenVectorState(m.home)
}

func (m *world) migratedToCutover() error {
	if err := m.mustRun("roca migrate"); err != nil {
		return err
	}
	if err := m.mustRun("roca migrate --status"); err != nil {
		return err
	}
	if !strings.Contains(m.last.stdout, "data2-memory-custody state=verified destination=memory_records") {
		return fmt.Errorf("memory custody is not verified:\n%s", m.last.stdout)
	}
	path := filepath.Join(m.home, ".roca", "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, "\n[layout]\nserving = \"cutover\"\n"...), 0o600)
}

func (m *world) opsSidecarHas(chunks string) error {
	var status struct {
		Databases []struct {
			Plugin         string `json:"plugin"`
			EmbeddedChunks *int64 `json:"embedded_chunks"`
			State          string `json:"state"`
		} `json:"databases"`
	}
	if err := json.Unmarshal([]byte(m.last.stdout), &status); err != nil {
		return fmt.Errorf("vector status is not JSON: %w\n%s", err, m.last.stdout)
	}
	for _, row := range status.Databases {
		if row.Plugin != "roca-ops" {
			continue
		}
		embedded := row.EmbeddedChunks != nil && (chunks == "0") == (*row.EmbeddedChunks == 0)
		if !embedded || row.State == "outdated" {
			return fmt.Errorf("ops sidecar, want %s embedded chunks and not outdated:\n%s", chunks, m.last.stdout)
		}
		return nil
	}
	return fmt.Errorf("vector status has no ops sidecar:\n%s", m.last.stdout)
}

func (m *world) memoryHitsAreOne(words, database string) error {
	if err := requireVectorExecuted(m.last.stdout); err != nil {
		return err
	}
	var answer struct {
		Results []struct {
			Database string `json:"database"`
			Table    string `json:"table"`
			Text     string `json:"text"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(m.last.stdout), &answer); err != nil {
		return err
	}
	var hits []string
	for _, result := range answer.Results {
		if strings.HasSuffix(result.Table, "memories") && strings.Contains(result.Text, words) {
			hits = append(hits, result.Database+"."+result.Table)
		}
	}
	if len(hits) != 1 || !strings.HasPrefix(hits[0], database+".") {
		return fmt.Errorf("memory hits holding %q = %v, want one from %s:\n%s", words, hits, database, m.last.stdout)
	}
	return nil
}
