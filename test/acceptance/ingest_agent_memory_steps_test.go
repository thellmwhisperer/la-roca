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

func registerIngestAgentMemorySteps(ctx *godog.ScenarioContext, w *ingestAcceptanceWorld) {
	ctx.Given(`^synthetic ZCode and Grok memory files are ready to ingest$`, w.seedAgentMemoryFiles)
	ctx.Given(`^ZCode and Grok session stores exist without memory directories$`, w.seedAgentStoresWithoutMemories)
	ctx.When(`^I run a human ingest dry-run$`, w.runHumanDryRun)
	ctx.Then(`^the dry-run shows ZCode saw more than one file and Grok memories$`, w.expectAgentMemoryDryRun)
	ctx.Then(`^ZCode and Grok each have memories and ZCode projects match their folders$`, w.expectAgentMemoryCensus)
	ctx.Then(`^the second ingest adds no ZCode or Grok memories$`, w.expectAgentMemoryZeroDelta)
	ctx.Then(`^the dry-run names the absent ZCode and Grok memory directories$`, w.expectAbsentAgentMemoryDirectories)
}

func (w *ingestAcceptanceWorld) seedAgentMemoryFiles() error {
	files := map[string]string{
		filepath.Join(w.home, ".zcode", "cli", "memories", "projects", "cobalt-harbor", "memory", "chart.md"): "" +
			"---\nname: synthetic-harbor\ndescription: how the synthetic harbor is kept\nmetadata:\n  type: feedback\n---\n" +
			"Keep the synthetic harbor chart in cobalt ink.\n",
		filepath.Join(w.home, ".zcode", "cli", "memories", "projects", "synthetic-default", "memory", "note.md"): "" +
			"---\nname: synthetic-default\ndescription: the other folder\n---\nA second synthetic ZCode fact.\n",
		filepath.Join(w.home, ".grok", "memory-v2", "global", "MEMORY.md"):                                    "Global synthetic tide table.\n",
		filepath.Join(w.home, ".grok", "memory-v2", "workspaces", "cobalt-harbor", "MEMORY.md"):               "Workspace synthetic tide table.\n",
		filepath.Join(w.home, ".grok", "memory-v2", "memory_state.sqlite"):                                    "not corpus\n",
		filepath.Join(w.home, ".grok", "memory-v2", "workspaces", "cobalt-harbor", "memory_state.sqlite-wal"): "not corpus\n",
	}
	for path, content := range files {
		if err := writeFixture(path, content); err != nil {
			return err
		}
	}
	return nil
}

func (w *ingestAcceptanceWorld) seedAgentStoresWithoutMemories() error {
	if err := writeFixture(filepath.Join(w.home, ".zcode", "cli", "db", "db.sqlite"), "route marker"); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(w.home, ".grok", "sessions"), 0o700)
}

func (w *ingestAcceptanceWorld) runHumanDryRun() error {
	_, err := w.runCommand("ingest", "--db-path", w.dbPath, "--dry-run")
	return err
}

func (w *ingestAcceptanceWorld) expectAgentMemoryDryRun() error {
	zcode, err := agentSourceLine(w.last.stdout, "zcode")
	if err != nil {
		return err
	}
	grok, err := agentSourceLine(w.last.stdout, "grok")
	if err != nil {
		return err
	}
	seen := quantityBefore(zcode, "files seen")
	memories := quantityBefore(grok, "memories")
	if memories == 0 {
		memories = quantityBefore(grok, "memory")
	}
	if seen <= 1 || memories <= 0 {
		return fmt.Errorf("dry-run zcode files=%d grok memories=%d\n%s", seen, memories, w.last.stdout)
	}
	return nil
}

func (w *ingestAcceptanceWorld) expectAgentMemoryCensus() error {
	for _, agent := range []string{"zcode", "grok"} {
		got, err := w.queryInt(`SELECT COUNT(*) FROM memories WHERE source_agent = ?`, agent)
		if err != nil {
			return err
		}
		if got == 0 {
			return fmt.Errorf("%s memories=%d, want > 0", agent, got)
		}
	}
	projects, err := w.queryStrings(`SELECT project FROM memories WHERE source_agent = 'zcode' ORDER BY project`)
	if err != nil {
		return err
	}
	if strings.Join(projects, " ") != "cobalt-harbor synthetic-default" {
		return fmt.Errorf("zcode projects = %v", projects)
	}
	return nil
}

func (w *ingestAcceptanceWorld) expectAgentMemoryZeroDelta() error {
	if !strings.Contains(w.last.stdout, "delta: memories=0") {
		return fmt.Errorf("second ingest delta is not zero:\n%s", w.last.stdout)
	}
	for _, agent := range []string{"zcode", "grok"} {
		line, err := agentSourceLine(w.last.stdout, agent)
		if err != nil {
			return err
		}
		if !strings.Contains(line, "0 memories") {
			return fmt.Errorf("%s second ingest wrote memories:\n%s", agent, line)
		}
	}
	return nil
}

func (w *ingestAcceptanceWorld) expectAbsentAgentMemoryDirectories() error {
	for _, reason := range []string{
		"ZCode project memory directory is absent",
		"Grok memory-v2 directory is absent",
	} {
		if !strings.Contains(w.last.stdout, reason) {
			return fmt.Errorf("dry-run did not name %q:\n%s", reason, w.last.stdout)
		}
	}
	return nil
}

func agentSourceLine(stdout, agent string) (string, error) {
	prefix := "✓ " + agent + " "
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return line, nil
		}
	}
	return "", fmt.Errorf("no %s source line:\n%s", agent, stdout)
}

func quantityBefore(line, label string) int {
	at := strings.Index(line, label)
	if at < 0 {
		return 0
	}
	fields := strings.Fields(line[:at])
	if len(fields) == 0 {
		return 0
	}
	raw := strings.ReplaceAll(fields[len(fields)-1], ",", "")
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}
