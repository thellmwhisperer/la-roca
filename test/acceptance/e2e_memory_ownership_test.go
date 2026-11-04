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
	ctx.Given(`^a home made by roca init with vector = (true|false)( and roca_ops = false)?, over an agent session that carries a memory mentioning "([^"]*)"$`,
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
	ctx.Then(`^the last vector worker line says "([^"]*)" and the worker log holds no "([^"]*)"$`, m.workerLogEnds)
	ctx.Then(`^vector status lists no "([^"]*)" database and no state "([^"]*)" or "([^"]*)"$`, m.statusListsNo)
	ctx.Then(`^a query hit from "([^"]*)" holds "([^"]*)"$`, m.queryHitHolds)
}

func (m *world) workerLogEnds(want, absent string) error {
	raw, err := os.ReadFile(filepath.Join(m.home, ".roca", "plugins", "roca-vector", "state", "worker.log"))
	if err != nil {
		return err
	}
	log := string(raw)
	last := ""
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "vector worker:") {
			last = line
		}
	}
	if !strings.Contains(last, want) || strings.Contains(log, absent) {
		return fmt.Errorf("worker log, want last worker line with %q and no %q:\n%s", want, absent, log)
	}
	return nil
}

func (m *world) statusListsNo(plugin, state, other string) error {
	rows, err := m.vectorStatusRows()
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Plugin == plugin || row.State == state || row.State == other {
			return fmt.Errorf("vector status lists %s in state %s:\n%s", row.Plugin, row.State, m.last.stdout)
		}
	}
	return nil
}

func (m *world) queryHitHolds(source, text string) error {
	var result struct {
		Hits []struct {
			Source  string `json:"source"`
			Snippet string `json:"snippet"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(m.last.stdout), &result); err != nil {
		return fmt.Errorf("roca query is not JSON: %w\n%s", err, m.last.stdout)
	}
	for _, hit := range result.Hits {
		if strings.HasPrefix(hit.Source, source) && strings.Contains(hit.Snippet, text) {
			return nil
		}
	}
	return fmt.Errorf("no query hit from %s holds %q:\n%s", source, text, m.last.stdout)
}

func (m *world) homeWithSessionMemory(vector, opsOff, words string) error {
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
	// init keeps a config that is already there, so this home is made with that vector.
	config := "[features]\nvector = " + vector + "\n"
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
	return m.servingCutoverByHand()
}

type vectorStatusRow struct {
	Plugin         string `json:"plugin"`
	EmbeddedChunks *int64 `json:"embedded_chunks"`
	State          string `json:"state"`
}

func (m *world) vectorStatusRows() ([]vectorStatusRow, error) {
	var status struct {
		Databases []vectorStatusRow `json:"databases"`
	}
	if err := json.Unmarshal([]byte(m.last.stdout), &status); err != nil {
		return nil, fmt.Errorf("vector status is not JSON: %w\n%s", err, m.last.stdout)
	}
	return status.Databases, nil
}

func (m *world) opsSidecarHas(chunks string) error {
	rows, err := m.vectorStatusRows()
	if err != nil {
		return err
	}
	for _, row := range rows {
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
