package cli

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/skill"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestSkillBareListsEveryRuntimePath(t *testing.T) {
	home := skillTestHome(t)
	var output strings.Builder
	runSkill(t, &output, "skill")
	text := output.String()
	if !strings.HasPrefix(text, fmt.Sprintf("rows[%d]{runtime,skill,path}:\n", 4*len(skill.Runtimes()))) {
		t.Fatalf("listing is not the stable TOON shape:\n%s", text)
	}
	for _, runtime := range skill.Runtimes() {
		if !strings.Contains(text, "\n  "+runtime+",") {
			t.Errorf("listing missing runtime %q:\n%s", runtime, text)
		}
	}
	for _, owned := range skill.OwnedNames() {
		if !strings.Contains(text, filepath.Join(home, ".claude", "skills", owned, "SKILL.md")) {
			t.Errorf("listing does not show the %s skill path:\n%s", owned, text)
		}
	}
}

func TestSkillInstallWritesUnderTempHome(t *testing.T) {
	home := skillTestHome(t)
	want := filepath.Join(home, ".claude", "skills", "roca", "SKILL.md")
	operationsPath := filepath.Join(home, ".claude", "skills", "roca-operations", "SKILL.md")
	vectorPath := filepath.Join(home, ".claude", "skills", "roca-vector", "SKILL.md")
	catalogPath := filepath.Join(home, ".claude", "skills", "roca-semantica", "SKILL.md")

	var output strings.Builder
	runSkill(t, &output, "skill", "install", "claude")
	for _, path := range []string{want, operationsPath, vectorPath, catalogPath} {
		if !strings.Contains(output.String(), "wrote "+path) {
			t.Fatalf("install did not narrate the write of %s:\n%s", path, output.String())
		}
	}
	zones, err := artifact.ParseFile(want)
	if err != nil || zones.System != skill.Content() || zones.User != "" {
		t.Fatalf("installed zones = %+v, err %v", zones, err)
	}
	// The playbook is read top to bottom, so the steps have to appear in the
	// order an agent performs them. Each one is looked for after the last, which
	// leaves prose free to name a command before the step that runs it.
	previous := 0
	for _, command := range []string{
		"curl -fsSL", "roca init", "roca query",
		"roca skill install", "roca vector status",
	} {
		offset := strings.Index(zones.System[previous:], command)
		if offset < 0 {
			t.Fatalf("installed agent playbook is missing %q, or puts it out of order", command)
		}
		previous += offset + len(command)
	}
	// The yes is a question `roca init` asks, not a second command and not a
	// hand-edited configuration file: an agent that is taught to switch a
	// feature on by hand switches it on for a machine with no index to serve.
	for _, forbidden := range []string{"plugins = true", "roca_ops = true", "vector = true",
		"roca init --vectors"} {
		if strings.Contains(zones.System, forbidden) {
			t.Fatalf("installed agent playbook still tells agents to set %q", forbidden)
		}
	}
	if zones, err := artifact.ParseFile(operationsPath); err != nil || zones.System != skill.OperationsContent() {
		t.Fatalf("installed operations zones = %+v, err %v", zones, err)
	}
	if zones, err := artifact.ParseFile(vectorPath); err != nil || zones.System != skill.VectorContent() {
		t.Fatalf("installed vector zones = %+v, err %v", zones, err)
	}
	catalogZones, err := artifact.ParseFile(catalogPath)
	if err != nil || !strings.Contains(catalogZones.System, "name: "+skill.CatalogName) {
		t.Fatalf("installed catalog zones = %+v, err %v", catalogZones, err)
	}
	registry, err := artifact.LoadRegistry(filepath.Join(home, ".roca", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := registry.Find("skill", "claude", want)
	if !ok || entry.SystemSHA256 != artifact.Checksum(skill.Content()) {
		t.Fatalf("registered skill = %+v, found %v", entry, ok)
	}
	catalogEntry, ok := registry.Find("skill-catalog", "claude", catalogPath)
	if !ok || catalogEntry.SystemSHA256 != artifact.Checksum(catalogZones.System) {
		t.Fatalf("registered catalog skill = %+v, found %v", catalogEntry, ok)
	}

	var again strings.Builder
	runSkill(t, &again, "skill", "install", "claude")
	for _, path := range []string{want, operationsPath, vectorPath, catalogPath} {
		if !strings.Contains(again.String(), "unchanged "+path) {
			t.Fatalf("reinstall did not report unchanged %s:\n%s", path, again.String())
		}
	}
}

// The two refusals an explicit install can meet are not the same. A registered
// file the operator deleted has no bytes of theirs to clobber and the install
// they typed is the consent, so it is simply written again; refusing it printed
// "unchanged" about a file that was not there and exited clean. A zoned file no
// registry record stands behind is still refused, because its SYSTEM zone was
// never proven to be ours — and it must not be reported as somebody's edit.
func TestSkillInstallRestoresARemovedFileAndRefusesAnUnregisteredOne(t *testing.T) {
	home := skillTestHome(t)
	path := filepath.Join(home, ".claude", "skills", "roca", "SKILL.md")
	var output strings.Builder
	runSkill(t, &output, "skill", "install", "claude")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	var restored, warning strings.Builder
	root := rootCommand(&cliEnv{out: &restored, errOut: &warning})
	root.SetArgs([]string{"skill", "install", "claude"})
	if err := root.Execute(); err != nil {
		t.Fatalf("reinstall over a removed file failed: %v", err)
	}
	if warning.String() != "" {
		t.Fatalf("an install the operator asked for by name warned instead of writing: %q", warning.String())
	}
	if !strings.Contains(restored.String(), "claude: wrote "+path) {
		t.Fatalf("the reinstall did not report the file it wrote:\n%s", restored.String())
	}
	if zones := installedZones(t, path); zones.System != skill.Content() {
		t.Fatalf("the reinstall did not restore the skill: %+v", zones)
	}

	if err := os.Remove(filepath.Join(home, ".roca", "artifacts.json")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, artifact.Zoned("an older release's system\n", "mine\n"))
	warning.Reset()
	root = rootCommand(&cliEnv{out: &restored, errOut: &warning})
	root.SetArgs([]string{"skill", "install", "claude"})
	if err := root.Execute(); err != nil {
		t.Fatalf("install over an unregistered skill failed: %v", err)
	}
	if !strings.Contains(warning.String(), "no record in La Roca's artifact registry") ||
		!strings.Contains(warning.String(), "skill install claude --force") {
		t.Fatalf("an unregistered artifact was reported as an edit: %q", warning.String())
	}
	if zones := installedZones(t, path); zones.System != "an older release's system\n" {
		t.Fatalf("an unregistered skill was replaced without consent: %+v", zones)
	}

	runSkill(t, &restored, "skill", "install", "claude", "--force")
	if zones := installedZones(t, path); zones.System != skill.Content() || zones.User != "mine\n" {
		t.Fatalf("the forced install did not replace SYSTEM and keep USER: %+v", zones)
	}
}

func installedZones(t *testing.T, path string) artifact.Zones {
	t.Helper()
	zones, err := artifact.ParseFile(path)
	if err != nil {
		t.Fatalf("installed skill has no zones: %v", err)
	}
	return zones
}

// One runtime whose file nothing can read never decides for the others, and
// the migration that replaced an operator's bytes says where they went.
func TestSkillInstallAllSurvivesOneUnreadableRuntimeAndNamesTheBackup(t *testing.T) {
	home := skillTestHome(t)
	broken := filepath.Join(home, ".codex", "skills", "roca", "SKILL.md")
	writeFile(t, broken, artifact.Zoned(skill.Content(), "")+"appended after the last marker\n")
	migrated := filepath.Join(home, ".hermes", "skills", "roca", "SKILL.md")
	writeFile(t, migrated, "an older release's skill\n")

	var out, warnings strings.Builder
	root := rootCommand(&cliEnv{out: &out, errOut: &warnings})
	root.SetArgs([]string{"skill", "install", "--all"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "skill install codex --force") {
		t.Fatalf("the unreadable runtime did not fail with its remedy: %v", err)
	}
	for _, runtime := range []string{"claude", "hermes", "opencode", "pi"} {
		if !strings.Contains(out.String(), runtime+": wrote ") {
			t.Fatalf("%s was skipped because codex could not be read:\n%s", runtime, out.String())
		}
	}
	if !strings.Contains(out.String(), migrated+" (replaced content kept at ") {
		t.Fatalf("the migration did not name the recovery copy:\n%s", out.String())
	}
}

func TestSkillInstallAllNarratesEveryPath(t *testing.T) {
	skillTestHome(t)
	var output strings.Builder
	runSkill(t, &output, "skill", "install", "--all")
	text := output.String()
	for _, runtime := range skill.Runtimes() {
		if !strings.Contains(text, runtime+": wrote ") {
			t.Errorf("missing write line for %s:\n%s", runtime, text)
		}
	}
}

func TestInitAnnouncesTheMustReadSkills(t *testing.T) {
	var output strings.Builder
	renderBootstrap(&cliEnv{out: &output}, service.InitResult{})
	text := output.String()
	for _, want := range []string{
		"must-read:", "`roca`", "`roca-operations`",
		"installed into every detected agent runtime",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("init does not announce %q:\n%s", want, text)
		}
	}
}

func TestInitInstallsEmbeddedSkillsIntoDetectedRuntimes(t *testing.T) {
	home := skillTestHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	before := skill.Detected(home, os.Getenv)
	if len(before) != 2 || before[0] != "claude" || before[1] != "cursor" {
		t.Fatalf("pre-init detected = %v, want [claude cursor]", before)
	}
	out := runRoot(t, Build{Version: "test", Commit: "test-sha"},
		"init", "--db-path", filepath.Join(home, ".roca", "roca.db"))
	for _, want := range []string{
		"must-read:", "`roca`", "`roca-operations`",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("init closing missed %q:\n%s", want, out)
		}
	}
	detected := skill.Detected(home, os.Getenv)
	if !slices.Contains(detected, "claude") || !slices.Contains(detected, "cursor") {
		t.Fatalf("claude or cursor vanished after init; detected = %v (was %v)", detected, before)
	}
	for _, runtime := range skill.Runtimes() {
		wanted := slices.Contains(detected, runtime)
		for _, name := range []string{skill.SkillName, skill.OperationsName, skill.VectorName} {
			path, err := skill.NamedPath(runtime, name, home, os.Getenv)
			if err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(path)
			if wanted && err != nil {
				t.Errorf("init did not install %s: %v", path, err)
			}
			if !wanted && !os.IsNotExist(err) {
				t.Errorf("init installed %s into a runtime that was not detected: %v", path, detected)
			}
		}
	}
	// The catalog is the map of what is searchable on this machine. An agent
	// that reads the skills after init and does not find it composes SQL by
	// guessing, so init writes it too and no manual step stands between the
	// first ingest and a good first question.
	for _, runtime := range []string{"claude", "cursor"} {
		path := filepath.Join(home, "."+runtime, "skills", "roca-semantica", "SKILL.md")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("init did not install the catalog skill at %s: %v", path, err)
		}
		if !strings.Contains(string(body), skill.CatalogName) {
			t.Errorf("the catalog skill at %s does not name itself:\n%s", path, body)
		}
	}
}

func TestIngestReseedsEmbeddedSkillsForANewRuntime(t *testing.T) {
	home := skillTestHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	runRoot(t, Build{Version: "test", Commit: "test-sha"},
		"init", "--db-path", filepath.Join(home, ".roca", "roca.db"))
	deleted := filepath.Join(home, ".claude", "skills", "roca", "SKILL.md")
	if err := os.Remove(deleted); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".grok"), 0o700); err != nil {
		t.Fatal(err)
	}
	runRoot(t, Build{Version: "test", Commit: "test-sha"}, "ingest")
	for _, name := range []string{skill.SkillName, skill.OperationsName, skill.VectorName} {
		path := filepath.Join(home, ".grok", "skills", name, "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("ingest did not reseed %s: %v", path, err)
		}
	}
	if _, err := os.Stat(deleted); !os.IsNotExist(err) {
		t.Fatal("ingest restored a deleted registered skill")
	}
}

func TestSkillTeachesTheInvestigationFunnel(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want []string
	}{
		{
			name: "investigation funnel",
			body: skill.OperationsContent(),
			want: []string{
				"## Investigation method", "Declare the purpose in one line",
				"roca explore --deep", "single bare word", "Read the terrain",
				"one concept per query", "FTS ANDs", "explicit OR", "whole corpus",
				"--sql-only", "roca exec", "Verdict grounded in rows",
			},
		},
		{
			name: "database scoping",
			body: skill.OperationsContent(),
			want: []string{
				"## Which databases a question sees",
				"--databases", "corpus,ops", "--databases all",
				"does not guess relevance",
				"inventory of the other attached names",
				"second SQL pass",
			},
		},
		{
			name: "search craft branch",
			body: skill.OperationsContent(),
			want: []string{
				"## Search craft",
				"completion.json",
				"finished_at",
				"Vector readiness is per selected sidecar",
				"it is not a readiness prerequisite for every\nquery",
				"still returns successfully with notices",
				"unchanged FTS/SQL path",
				"the hybrid loop is mandatory",
				"complete working path",
				"last resort",
				"Agents never pass `--full`",
				"plugin_roca_ops.memories",
				"Invite the user to build the index",
			},
		},
		{
			name: "hybrid loop",
			body: skill.OperationsContent(),
			want: []string{
				"## Hybrid loop",
				"Vector search finds the nearby rows",
				"FTS censuses them",
				"SQL frames them",
				"the shipped RRF hybrid",
				"inference only at the end",
				`roca vector query "<first-person phrase or bare word>" 100`,
				"names of people", "my boss is named",
				"k must be between 1 and 100",
				"LIKE '%term%'",
				"COUNT(DISTINCT e.session_id)",
				"COUNT(DISTINCT e.id)",
				"Use them only as a last resort when you cannot write the SELECT yourself",
			},
		},
		{
			name: "vector owns the index",
			body: skill.VectorContent(),
			want: []string{
				"There is no separate command to start one",
				"roca vector status",
				"roca vector install",
				"roca vector ingest --delta",
				"roca vector compact",
				"completion.json",
				"invite the user to",
				"is not an\nempty product",
				"never to decide whether the index",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, want := range test.want {
				if !strings.Contains(test.body, want) {
					t.Errorf("skill lacks %q", want)
				}
			}
		})
	}
	operations := skill.OperationsContent()
	for _, forbidden := range []string{
		"Declared sidecars are ready only when",
		"refuses when no declared sidecar is ready",
	} {
		if strings.Contains(operations, forbidden) {
			t.Errorf("operations skill must not teach %q", forbidden)
		}
	}
	hybrid := markdownSection(skill.OperationsContent(), "## Hybrid loop")
	if strings.Contains(hybrid, `roca query --sql-only "`) {
		t.Fatal("hybrid loop sends its doctrinal path through model inference")
	}
	if strings.Contains(hybrid, "roca vector vocab") {
		t.Fatal("hybrid loop still teaches vocab discovery")
	}
	if strings.Contains(skill.VectorContent(), "## Hybrid loop") ||
		strings.Contains(skill.VectorContent(), "## Hybrid discovery") {
		t.Fatal("vector skill still teaches the hybrid loop")
	}
	for _, embedded := range skill.EmbeddedSkills() {
		if strings.Contains(embedded.Body, "roca vector vocab") {
			t.Errorf("%s still teaches roca vector vocab", embedded.Name)
		}
	}
}

func markdownSection(body, heading string) string {
	start := strings.Index(body, heading)
	if start < 0 {
		return ""
	}
	rest := body[start+len(heading):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		return rest[:end]
	}
	return rest
}

func skillTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{
		"CLAUDE_CONFIG_DIR", "CODEX_HOME", "CURSOR_HOME", "GROK_HOME", "OPENCODE_CONFIG",
		"HERMES_HOME", "PI_CODING_AGENT_DIR", "QWEN_HOME",
	} {
		t.Setenv(key, "")
	}
	return home
}

func runSkill(t *testing.T, out *strings.Builder, args ...string) {
	t.Helper()
	root := rootCommand(&cliEnv{out: out})
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("roca %s: %v", strings.Join(args, " "), err)
	}
}

// Installing a plugin teaches every runtime that asked for the skills: the
// semantic catalog a `roca skill install` placed is regenerated from the
// plugin set the lifecycle just changed, and a runtime that never asked is
// left without one.
func TestPluginLifecycleRefreshesTheInstalledCatalogSkill(t *testing.T) {
	home := skillTestHome(t)
	var output strings.Builder
	runSkill(t, &output, "skill", "install", "claude")
	catalogPath := filepath.Join(home, ".claude", "skills", "roca-semantica", "SKILL.md")
	before, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), "synthetic-refresh") {
		t.Fatalf("the fresh catalog already names a plugin that is not installed:\n%s", before)
	}

	paths := resolvedIn(t, home)
	directory := filepath.Join(pluginRoot(paths), "synthetic-refresh")
	writeFile(t, filepath.Join(directory, "semantic.yaml"), `version: 1
description: Synthetic records refreshed into the catalog.
questions: ["Which synthetic records exist?"]
tables:
  - name: records
    description: Synthetic records.
    columns: [id, value]
`)
	database, err := sql.Open("sqlite", filepath.Join(directory, "plugin.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TABLE records (id INTEGER PRIMARY KEY, value TEXT)"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	env := &cliEnv{out: io.Discard, errOut: io.Discard}
	env.refreshPluginContracts()

	after, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		"## synthetic-refresh (alias plugin_synthetic_refresh)",
		"Synthetic records refreshed into the catalog.",
		"### records · plugin_synthetic_refresh.records",
		"Columns: id, value",
	} {
		if !strings.Contains(string(after), needle) {
			t.Errorf("the refreshed catalog missing %q:\n%s", needle, after)
		}
	}
	unregistered := filepath.Join(home, ".codex", "skills", "roca-semantica", "SKILL.md")
	if _, err := os.Stat(unregistered); !os.IsNotExist(err) {
		t.Errorf("a runtime that never asked for skills received a catalog: %v", err)
	}
}

func TestPluginContractRefreshRegistersUpdatesAndUnregistersVectorSurfaces(t *testing.T) {
	home := skillTestHome(t)
	paths := resolvedIn(t, home)
	root := pluginRoot(paths)
	writeFile(t, paths.Config, "[features]\nplugins = true\n")
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, plugin.PackageFilename)
	manifest := `{
  "schema": 1,
  "name": "synthetic-vector",
  "version": "1.0.0",
  "binary": "roca",
  "databases": [{
    "name": "records",
    "path": "records.db",
    "alias": "plugin_synthetic_vector",
    "attachment": "resident",
    "retention": "The plugin retains synthetic records."
  }],
  "semantic": {"databases": [{
    "database": "records",
    "description": "Synthetic prose records.",
    "questions": ["Which synthetic records exist?"],
    "tables": [{
      "name": "records",
      "description": "One synthetic record.",
      "columns": ["id", "title", "body", "telemetry", "occurred_at"]
    }]
  }]},
  "vector": {"databases": [{
    "database": "records",
    "tables": [{"name": "records", "id_column": "id", "text_columns": ["title", "body"], "time_columns": ["occurred_at"]}]
  }]},
  "verbs": [],
  "capabilities": []
}`
	writeFile(t, manifestPath, manifest)
	database, err := sql.Open("sqlite", filepath.Join(directory, "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE records (
		id INTEGER PRIMARY KEY, title TEXT, body TEXT, telemetry TEXT, occurred_at TEXT)`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	writePluginChecksums(t, directory, plugin.PackageFilename, "records.db")

	var output strings.Builder
	env := &cliEnv{out: &output, errOut: &output}
	code, err := executeWithEnv(env,
		[]string{"plugin", "--yes", "install", directory}, strings.NewReader(""))
	if err != nil || code != ExitOK {
		t.Fatalf("plugin install = code %d, err %v, output %q", code, err, output.String())
	}
	registryPath := plugin.VectorRegistryPath(root)
	registry, err := plugin.LoadVectorRegistry(registryPath)
	if err != nil || len(registry.Databases) != 1 {
		t.Fatalf("registered vector surfaces = %+v, err = %v", registry, err)
	}
	registration := registry.Databases[0]
	if registration.Path != "records.db" || filepath.IsAbs(registration.Path) ||
		!slices.Equal(registration.Tables[0].TextColumns, []string{"title", "body"}) {
		t.Fatalf("vector registration = %+v", registration)
	}

	updated := strings.Replace(manifest,
		`"text_columns": ["title", "body"]`, `"text_columns": ["body"]`, 1)
	updated = strings.Replace(updated, `"version": "1.0.0"`, `"version": "1.1.0"`, 1)
	writeFile(t, manifestPath, updated)
	writePluginChecksums(t, directory, plugin.PackageFilename, "records.db")
	output.Reset()
	code, err = executeWithEnv(env,
		[]string{"plugin", "--yes", "update", "synthetic-vector"}, strings.NewReader(""))
	if err != nil || code != ExitOK {
		t.Fatalf("plugin update = code %d, err %v, output %q", code, err, output.String())
	}
	registry, err = plugin.LoadVectorRegistry(registryPath)
	if err != nil || !slices.Equal(registry.Databases[0].Tables[0].TextColumns, []string{"body"}) {
		t.Fatalf("updated vector surfaces = %+v, err = %v", registry, err)
	}

	output.Reset()
	code, err = executeWithEnv(env,
		[]string{"plugin", "--yes", "uninstall", "synthetic-vector"}, strings.NewReader(""))
	if err != nil || code != ExitOK {
		t.Fatalf("plugin uninstall = code %d, err %v, output %q", code, err, output.String())
	}
	registry, err = plugin.LoadVectorRegistry(registryPath)
	if err != nil || len(registry.Databases) != 0 {
		t.Fatalf("unregistered vector surfaces = %+v, err = %v", registry, err)
	}
}

func writePluginChecksums(t *testing.T, directory string, names ...string) {
	t.Helper()
	var checksums strings.Builder
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		fmt.Fprintf(&checksums, "%x  %s\n", digest, name)
	}
	writeFile(t, filepath.Join(directory, "checksums.txt"), checksums.String())
}

// A plugin lifecycle that cannot read the artifact registry must say so rather
// than leave every installed catalog stale in silence: the refresh contract is
// that a failure or a divergence is a warning, never a failed plugin command.
func TestPluginRefreshWarnsWhenTheRegistryCannotBeRead(t *testing.T) {
	home := skillTestHome(t)
	var output strings.Builder
	runSkill(t, &output, "skill", "install", "claude")

	writeFile(t, filepath.Join(home, ".roca", "artifacts.json"), "{not json")

	var warnings strings.Builder
	env := &cliEnv{out: io.Discard, errOut: &warnings}
	env.refreshPluginContracts()

	if !strings.Contains(warnings.String(), "the semantic catalog skill was not refreshed") {
		t.Fatalf("an unreadable registry was not warned: %q", warnings.String())
	}
}
