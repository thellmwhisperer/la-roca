package ingest

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

type syntheticContributionParser struct{}

func (syntheticContributionParser) Detect(file parsers.File) bool {
	return bytes.Contains(file.Content, []byte("synthetic-nova-session"))
}

func (syntheticContributionParser) Parse(parsers.File) (parsers.Records, error) {
	return parsers.Records{Sessions: []parsers.Session{{ID: "synthetic-nova"}}}, nil
}

func TestDetectedAgentsFollowExistingStores(t *testing.T) {
	home := t.TempDir()
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: home}, Settings{})
	if detected := DetectAgents(roots); detected == nil || len(detected) != 0 {
		t.Fatalf("empty machine detected agents = %#v, want []", detected)
	}

	for _, path := range []string{
		roots.ClaudeProjects,
		roots.ClaudeDesktopSessions,
		roots.CoworkSessions,
		roots.CodexRoot,
		roots.PiSessions,
		roots.GrokSessions,
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{roots.OpenCodeDB, roots.HermesDB} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	plan := Scan(roots)
	want := []string{"claude", "claude-desktop", "cowork", "codex", "opencode", "pi", "hermes", "grok"}
	if !slices.Equal(plan.DetectedAgents, want) {
		t.Fatalf("detected agents = %v, want %v", plan.DetectedAgents, want)
	}

	if err := os.RemoveAll(roots.CoworkSessions); err != nil {
		t.Fatal(err)
	}
	plan = Scan(roots)
	if slices.Contains(plan.DetectedAgents, "cowork") {
		t.Fatalf("absent Cowork was detected: %v", plan.DetectedAgents)
	}
}

func TestWorkspaceRootsResolveSessionIdentityWithoutBecomingContent(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "work")
	project := filepath.Join(workspace, "demo")
	settings := Settings{WorkspaceRoots: []string{workspace}}
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: home}, settings)
	encoded := encodeRoot(project)
	transcript := filepath.Join(roots.ClaudeProjects, encoded,
		"99999999-8888-7777-6666-555555555555.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("never ingest this"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan := Scan(roots)
	if _, found := plan.Scanned["config_files"]; found {
		t.Fatalf("repository configuration is still a source: %v", plan.Scanned)
	}
	for _, target := range plan.Targets {
		if target.Path == transcript && target.Project != "demo" {
			t.Fatalf("session project = %q, want demo", target.Project)
		}
		if target.Path == filepath.Join(project, "AGENTS.md") {
			t.Fatal("the repository AGENTS.md became ingest content")
		}
	}
}

func TestTheLocalBinaryRunnerIsNeverIngested(t *testing.T) {
	home := t.TempDir()
	runner := filepath.Join(home, ".roca", "runner")
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: home}, Settings{RunnerDir: runner})
	transcript := filepath.Join(roots.ClaudeProjects, encodeRoot(runner),
		"99999999-8888-7777-6666-555555555555.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("synthetic private inference\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := Scan(roots)
	for _, target := range plan.Targets {
		if target.Path == transcript {
			t.Fatal("La Roca's own inference session entered the ingest plan")
		}
	}
	if len(plan.Excluded) != 1 || plan.Excluded[0].Path != transcript {
		t.Fatalf("runner transcript was not explicitly excluded: %+v", plan.Excluded)
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("the excluded runner created an operator warning: %v", plan.Warnings)
	}
}

func TestRegisteredParserLocationsFeedTheIngestPlan(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".nova", "sessions")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	wanted := filepath.Join(root, "nested", "kept.source")
	for path, content := range map[string]string{
		wanted:                                "synthetic-nova-session",
		filepath.Join(root, "foreign.source"): "another agent",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	registered := parsers.Registration{
		Name: "nova", SourceAgent: "nova", Locations: []string{".nova/sessions"},
		Destination: parsers.DestinationCorpus, Parser: syntheticContributionParser{},
	}
	plan := Plan{Scanned: map[string]int{}}
	addRegisteredParsers(Roots{Home: home}, &plan, []parsers.Registration{registered})
	if len(plan.Targets) != 2 {
		t.Fatalf("contributed targets = %+v", plan.Targets)
	}
	var claimed []string
	excluded := 0
	for _, target := range plan.Targets {
		content, err := os.ReadFile(target.Path)
		if err != nil {
			t.Fatal(err)
		}
		if target.Kind != parsers.Kind("nova") || target.SourceAgent != "nova" {
			t.Fatalf("contributed target = %+v", target)
		}
		records, err := registered.Parse(parsers.File{Content: content,
			Meta: parsers.FileMeta{Path: target.Path, SourceAgent: target.SourceAgent}})
		if err != nil {
			t.Fatal(err)
		}
		if len(records.Sessions) == 1 {
			claimed = append(claimed, target.Path)
		} else if len(records.Discards) == 1 && records.Discards[0].ByDesign {
			excluded++
		}
	}
	if !slices.Equal(claimed, []string{wanted}) {
		t.Fatalf("claimed targets = %v, want %s", claimed, wanted)
	}
	if excluded != 1 {
		t.Fatalf("excluded targets = %d, want the foreign candidate", excluded)
	}
	if plan.Scanned["nova_files"] != 2 || !slices.Contains(plan.DetectedAgents, "nova") {
		t.Fatalf("contributed plan = %+v", plan)
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("a well declared location warned the operator: %v", plan.Warnings)
	}
}

// TestContributedLocationsStayInsideTheirDeclaredStore pins the two halves of
// the contribution boundary: a location that would widen the walk beyond an
// agent's session store is refused out loud, and a location that is merely
// absent or empty still reports its counter at zero rather than vanishing from
// the report as a root nobody looked at.
func TestContributedLocationsStayInsideTheirDeclaredStore(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "private.source"),
		[]byte("synthetic-nova-session"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".nova", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name        string
		locations   []string
		wantWarning bool
	}{
		{"an empty location", []string{""}, true},
		{"the home directory itself", []string{"."}, true},
		{"a location climbing out of home", []string{"../.."}, true},
		{"the filesystem root", []string{string(filepath.Separator)}, true},
		{"an absent store", []string{".nova/sessions"}, false},
		{"an existing but empty store", []string{".nova/empty"}, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			plan := Plan{Scanned: map[string]int{}}
			addRegisteredParsers(Roots{Home: home}, &plan, []parsers.Registration{{
				Name: "nova", SourceAgent: "nova", Locations: testCase.locations,
				Destination: parsers.DestinationCorpus, Parser: syntheticContributionParser{},
			}})
			if len(plan.Targets) != 0 {
				t.Fatalf("contributed targets = %+v", plan.Targets)
			}
			count, registeredKey := plan.Scanned["nova_files"]
			if !registeredKey || count != 0 {
				t.Fatalf("nova_files = %d (present %t), want a registered zero", count, registeredKey)
			}
			if warned := len(plan.Warnings) > 0; warned != testCase.wantWarning {
				t.Fatalf("warnings = %v, want a warning %t", plan.Warnings, testCase.wantWarning)
			}
		})
	}
}

func TestContributedRelativeLocationWithoutHomeIsReported(t *testing.T) {
	plan := Plan{Scanned: map[string]int{}}
	addRegisteredParsers(Roots{}, &plan, []parsers.Registration{{
		Name: "nova", SourceAgent: "nova", Locations: []string{".nova/sessions"},
		Destination: parsers.DestinationCorpus, Parser: syntheticContributionParser{},
	}})
	if count, ok := plan.Scanned["nova_files"]; !ok || count != 0 {
		t.Fatalf("nova_files = %d (present %t), want a registered zero", count, ok)
	}
	if len(plan.Warnings) != 1 {
		t.Fatalf("warnings = %v, want the unusable relative location", plan.Warnings)
	}
}

// The Grok store files sessions by the URL-escaped working directory they ran
// in. The escape is lossless, so the project decodes from the directory name
// alone, and the La Roca runner store is excluded exactly as every other
// source's is.
func TestGrokSessionsDecodeTheirProjectAndTheRunnerIsExcluded(t *testing.T) {
	home := t.TempDir()
	w := &world{
		home:      home,
		workspace: filepath.Join(home, "w"),
		env:       Environment{GOOS: "darwin", Home: home},
		settings: Settings{
			WorkspaceRoots: []string{filepath.Join(home, "w")},
			RunnerDir:      filepath.Join(home, ".roca", "runner"),
		},
	}
	roots := w.roots()
	project := filepath.Join(w.workspace, "demo")
	session := "99999999-8888-7777-6666-555555555555"
	// The runner store is written with lowercase percent hex, which decodes to
	// the same path but is not the escaping Go emits: the exclusion holds on what
	// the name means and never on one encoder's conventions.
	runnerDir := strings.ReplaceAll(url.PathEscape(roots.RunnerDir), "%2F", "%2f")
	for _, dir := range []string{
		filepath.Join(roots.GrokSessions, url.PathEscape(project), session),
		filepath.Join(roots.GrokSessions, runnerDir, session),
	} {
		w.write(t, filepath.Join(dir, "summary.json"),
			`{"info":{"id":"grok-fixture-1","cwd":"`+project+`"}}`)
		w.write(t, filepath.Join(dir, "updates.jsonl"),
			`{"method":"session/update","params":{"sessionId":"grok-fixture-1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"synthetic question"},"_meta":{"promptIndex":1}}},"timestamp":1785592800}`+"\n")
	}

	plan := Scan(roots)
	var decoded *Target
	var excluded *Target
	runnerTranscript := filepath.Join(roots.GrokSessions, runnerDir, session, "updates.jsonl")
	for i := range plan.Targets {
		target := &plan.Targets[i]
		if target.Kind != parsers.KindGrokSession {
			continue
		}
		if target.Path == runnerTranscript {
			t.Fatal("the runner Grok transcript entered the ingest plan")
		}
		if filepath.Base(target.Path) == "updates.jsonl" && target.SessionID == session {
			decoded = target
		}
	}
	for i := range plan.Excluded {
		if strings.Contains(plan.Excluded[i].Path, "runner") {
			excluded = &plan.Excluded[i]
		}
	}
	if decoded == nil {
		t.Fatal("the demo Grok transcript was not scanned")
	}
	if decoded.Project != "demo" {
		t.Errorf("project = %q, want demo", decoded.Project)
	}
	if decoded.SidecarPath == "" || !strings.HasSuffix(decoded.SidecarPath, "summary.json") {
		t.Errorf("sidecar = %q, want the paired summary.json", decoded.SidecarPath)
	}
	if excluded == nil {
		t.Fatal("the runner Grok session was not excluded")
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("the excluded runner created an operator warning: %v", plan.Warnings)
	}
}
