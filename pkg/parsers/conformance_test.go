package parsers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type conformanceFixture struct {
	Parser      string          `json:"parser"`
	Destination string          `json:"destination"`
	Source      string          `json:"source"`
	Meta        FileMeta        `json:"meta"`
	Want        conformanceWant `json:"want"`
}

type conformanceWant struct {
	Sessions []conformanceSession `json:"sessions"`
	Memories []conformanceMemory  `json:"memories"`
}

type conformanceSession struct {
	ID            string                `json:"id"`
	SourceAgent   string                `json:"source_agent"`
	Project       string                `json:"project,omitempty"`
	Title         string                `json:"title,omitempty"`
	ParentID      string                `json:"parent_id,omitempty"`
	OrphanedTools []conformanceTool     `json:"orphaned_tools,omitempty"`
	Exchanges     []conformanceExchange `json:"exchanges"`
}

type conformanceExchange struct {
	HumanText string            `json:"human_text"`
	AgentText string            `json:"agent_text"`
	Tools     []conformanceTool `json:"tools,omitempty"`
}

type conformanceTool struct {
	CallID         string `json:"call_id,omitempty"`
	Name           string `json:"name"`
	ParamsSummary  string `json:"params_summary,omitempty"`
	HadError       bool   `json:"had_error,omitempty"`
	ErrorMessage   string `json:"error_message,omitempty"`
	InitiativeType string `json:"initiative_type,omitempty"`
}

type conformanceMemory struct {
	Layer       string `json:"layer"`
	Content     string `json:"content"`
	SourceAgent string `json:"source_agent"`
	Project     string `json:"project,omitempty"`
}

// syntheticHome stands in for the operator's home while the harness checks that
// a registry line's declared locations resolve to a store and not to the machine.
const syntheticHome = "/synthetic/home"

var expectedCanonicalHarnesses = map[Kind]string{
	KindClaudeSession:           "Claude Code",
	KindClaudeMemory:            "Claude Code",
	KindSessionMetadata:         "Claude Desktop",
	KindCoworkAudit:             "Cowork",
	KindCodexSession:            "Codex CLI",
	KindCodexHistory:            "Codex CLI",
	KindCodexFile:               "Codex CLI",
	KindCodexMemoryAggregate:    "Codex CLI",
	KindSubagent:                "Claude Code",
	KindPiSession:               "Pi",
	KindQwenCode:                "Qwen Code",
	KindGLMSkill:                "GLM",
	KindClaudeWebConversations:  "Claude Web",
	KindClaudeWebMemories:       "Claude Web",
	KindClaudeWebProjects:       "Claude Web",
	KindClaudeWebDesignChats:    "Claude Web",
	KindChatGPTWebConversations: "ChatGPT",
	KindChatGPTCodex:            "Codex CLI",
	KindCursorDB:                "Cursor",
	KindCursorStore:             "Cursor",
	KindGrokSession:             "Grok Build",
	KindGrokSessionMetadata:     "Grok Build",
	KindHermesMemory:            "Hermes",
}

// TestRegisteredParsersConform is the contribution harness. Every directory in
// testdata/conformance is a complete, synthetic worked example: its manifest
// names a registered parser, its own source file, the declared destination and
// the normalized records that must result.
func TestRegisteredParsersConform(t *testing.T) {
	fixtures := conformanceFixturePaths(t)

	covered := map[string]bool{}
	registeredNames := map[string]bool{}
	for _, registered := range Registered() {
		if registered.Name == "" || registered.CanonicalHarness == "" || registered.Parser == nil {
			t.Fatalf("incomplete parser registration: %+v", registered)
		}
		if want, ok := expectedCanonicalHarnesses[Kind(registered.Name)]; !ok || registered.CanonicalHarness != want {
			t.Fatalf("parser %q harness = %q, want %q",
				registered.Name, registered.CanonicalHarness, want)
		}
		if registeredNames[registered.Name] {
			t.Fatalf("parser %q is registered more than once", registered.Name)
		}
		registeredNames[registered.Name] = true
		if _, refused := registered.ResolveLocations(syntheticHome); len(refused) > 0 {
			t.Fatalf("parser %q declares the unusable locations %v", registered.Name, refused)
		}
		if _, refused := registered.ResolveHarvestLocations(syntheticHome); len(refused) > 0 {
			t.Fatalf("parser %q declares the unusable real-harvest locations %v",
				registered.Name, refused)
		}
	}
	for _, path := range fixtures {
		path := path
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			fixture := readConformanceFixture(t, path)
			registered, ok := Lookup(fixture.Parser)
			if !ok {
				t.Fatalf("parser %q has no registry line", fixture.Parser)
			}
			covered[registered.Name] = true
			if got := registered.Destination.String(); got != fixture.Destination {
				t.Fatalf("destination = %q, want %q", got, fixture.Destination)
			}

			content, err := os.ReadFile(filepath.Join(filepath.Dir(path), fixture.Source))
			if err != nil {
				t.Fatal(err)
			}
			file := File{Content: content, Meta: fixture.Meta}
			if !registered.Parser.Detect(file) {
				t.Fatal("detector rejected its own synthetic fixture")
			}
			foreign := File{Content: []byte(`{"synthetic_foreign_fixture":true}`),
				Meta: FileMeta{SourceAgent: "synthetic-foreign"}}
			if registered.Parser.Detect(foreign) {
				t.Fatal("detector claimed the foreign control fixture")
			}

			records, err := registered.Parse(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := registered.Destination.Conforms(records); err != nil {
				t.Fatalf("destination routing: %v", err)
			}
			for _, session := range records.Sessions {
				want := CanonicalHarness(Kind(registered.Name), session.SourceAgent)
				if session.SourceSurface != want || want == "" {
					t.Errorf("session %q harness = %q, want %q",
						session.ID, session.SourceSurface, want)
				}
			}
			for _, memory := range records.Memories {
				want := CanonicalHarness(Kind(registered.Name), memory.SourceAgent)
				if memory.SourceSurface != want || want == "" {
					t.Errorf("memory %q harness = %q, want %q",
						memory.FilePath, memory.SourceSurface, want)
				}
			}
			if got := conformanceProjection(records); !reflect.DeepEqual(got, fixture.Want) {
				gotJSON, _ := json.MarshalIndent(got, "", "  ")
				wantJSON, _ := json.MarshalIndent(fixture.Want, "", "  ")
				t.Fatalf("normalized records differ\ngot:  %s\nwant: %s", gotJSON, wantJSON)
			}
		})
	}

	for _, registered := range Registered() {
		if !covered[registered.Name] {
			t.Errorf("registered parser %q has no synthetic conformance fixture", registered.Name)
		}
	}
}

// realHarvestEnv opts this machine into the real-surface smoke. The stores it
// walks are the operator's own private conversation history, so the shared gate
// never reads them and no contributor is judged on what an unrelated agent left
// in their home directory. A parser author runs it deliberately, on a machine
// where the agent is installed, and reports its yield in the pull request.
const realHarvestEnv = "ROCA_REAL_HARVEST"

// TestRegisteredParsersHarvestPresentAgentStores is the real-surface guard the
// synthetic catalogue cannot provide. A contribution's Locations opt it in;
// established scanners may opt in with HarvestLocations. When ROCA_REAL_HARVEST
// asks for it and that agent store exists on the machine, the harness walks it
// read-only, asks Detect about every regular file, parses every claim and
// reports the normalized yield. A detector that chose a tiny secondary surface
// in a large store cannot pass on the author's own machine merely because its
// invented fixture agrees with it.
func TestRegisteredParsersHarvestPresentAgentStores(t *testing.T) {
	if os.Getenv(realHarvestEnv) != "1" {
		t.Skip("set " + realHarvestEnv + "=1 to walk the present agent stores read-only")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, registered := range Registered() {
		if len(registered.Locations) == 0 && len(registered.HarvestLocations) == 0 {
			continue
		}
		registered := registered
		t.Run(registered.Name, func(t *testing.T) {
			roots, refused := registered.ResolveHarvestLocations(home)
			if len(refused) > 0 {
				t.Fatalf("real-harvest locations are unusable: %v", refused)
			}
			present := false
			files, detected, unreadableFiles := 0, 0, 0
			var bytes, unreadableBytes, largestDetectedBytes int64
			largestDetectedExchanges := 0
			yield := realHarvestYield{}
			for _, root := range roots {
				if _, err := os.Stat(root); os.IsNotExist(err) {
					continue
				} else if err != nil {
					t.Fatalf("inspect real store %s: %v", root, err)
				}
				present = true
				err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
					if walkErr != nil {
						unreadableFiles++
						return nil
					}
					if !entry.Type().IsRegular() {
						return nil
					}
					info, err := entry.Info()
					if err != nil {
						unreadableFiles++
						return nil
					}
					files++
					bytes += info.Size()
					content, err := os.ReadFile(path)
					if err != nil {
						unreadableFiles++
						unreadableBytes += info.Size()
						return nil
					}
					source := registered.SourceAgent
					if source == "" {
						source = registered.Name
					}
					file := File{Content: content, Meta: FileMeta{
						Path: path, FileName: filepath.Base(path), SourceAgent: source,
					}}
					if !registered.Parser.Detect(file) {
						return nil
					}
					detected++
					records, err := registered.Parse(file)
					if err != nil {
						return err
					}
					fileExchanges := 0
					for _, session := range records.Sessions {
						fileExchanges += len(session.Exchanges)
					}
					if info.Size() > largestDetectedBytes {
						largestDetectedBytes = info.Size()
						largestDetectedExchanges = fileExchanges
					}
					yield.add(records)
					return nil
				})
				if err != nil {
					t.Fatalf("read-only real harvest under %s: %v", root, err)
				}
			}
			if !present {
				t.Skip("agent store is not present on this machine")
			}
			t.Logf("real harvest: store_files=%d store_bytes=%d detected_files=%d "+
				"sessions=%d exchanges=%d memories=%d thinking=%d tools=%d discards=%d deferred=%d "+
				"largest_detected_file_bytes=%d largest_detected_file_exchanges=%d unreadable_files=%d",
				files, bytes, detected, yield.sessions, yield.exchanges, yield.memories,
				yield.thinking, yield.tools, yield.discards, yield.deferred,
				largestDetectedBytes, largestDetectedExchanges, unreadableFiles)
			if files > 0 && detected == 0 {
				t.Fatal("detector found no real files in the declared agent store")
			}
			if yield.nearZero(bytes - unreadableBytes) {
				t.Fatalf("near-zero real harvest from a large store: %d exchanges and %d memories from %d bytes",
					yield.exchanges, yield.memories, bytes)
			}
		})
	}
}

type realHarvestYield struct {
	sessions, exchanges, memories int
	thinking, tools, discards     int
	deferred                      int
}

func (y *realHarvestYield) add(records Records) {
	y.sessions += len(records.Sessions)
	y.memories += len(records.Memories)
	y.discards += len(records.Discards)
	y.deferred += records.Deferred
	for _, session := range records.Sessions {
		y.exchanges += len(session.Exchanges)
		y.tools += len(session.OrphanedTools)
		for _, exchange := range session.Exchanges {
			y.thinking += len(exchange.Thinking)
			y.tools += len(exchange.Tools)
		}
	}
}

func (y realHarvestYield) nearZero(storeBytes int64) bool {
	return storeBytes >= 1<<20 && y.exchanges+y.memories < 10
}

func TestLargeRealStoreCannotPassWithNearZeroYield(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		bytes     int64
		exchanges int
		memories  int
		want      bool
	}{
		{"large store with nine turns", 1 << 20, 9, 0, true},
		{"large store with ten turns", 1 << 20, 10, 0, false},
		{"large memory store", 2 << 20, 0, 12, false},
		{"small store", 1<<20 - 1, 0, 0, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			yield := realHarvestYield{exchanges: testCase.exchanges, memories: testCase.memories}
			if got := yield.nearZero(testCase.bytes); got != testCase.want {
				t.Errorf("nearZero() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestDestinationsRejectCrossSurfaceRecords(t *testing.T) {
	corpus := Records{Sessions: []Session{{ID: "synthetic-session"}}}
	store := Records{Memories: []Memory{{Content: "synthetic memory"}}}
	for _, testCase := range []struct {
		name        string
		destination Destination
		records     Records
		wantError   string
	}{
		{"corpus to corpus", DestinationCorpus, corpus, ""},
		{"store to corpus", DestinationCorpus, store, "corpus parser produced store memories"},
		{"store to store", DestinationStore, store, ""},
		{"corpus to store", DestinationStore, corpus, "store parser produced corpus sessions"},
		{"corpus to both", DestinationBoth, corpus, ""},
		{"store to both", DestinationBoth, store, ""},
		{"undeclared destination", 0, corpus, "parser declares invalid destination 0"},
		{"unknown destination bit", DestinationBoth | 1<<4, store,
			"parser declares invalid destination 19"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.destination.Conforms(testCase.records)
			if testCase.wantError == "" {
				if err != nil {
					t.Fatalf("Conforms() error = %v, want none", err)
				}
				return
			}
			if err == nil || err.Error() != testCase.wantError {
				t.Fatalf("Conforms() error = %v, want %q", err, testCase.wantError)
			}
		})
	}
}

// TestRegisteredDetectorsRejectEveryForeignFixture is what keeps an ownership
// marker honest. A detector that claims a file another parser owns is the
// failure the guide warns about, and the catalogue of synthetic fixtures is the
// only place it can be caught before a real machine pays for it.
func TestRegisteredDetectorsRejectEveryForeignFixture(t *testing.T) {
	catalogue := map[string]File{}
	for _, path := range conformanceFixturePaths(t) {
		fixture := readConformanceFixture(t, path)
		content, err := os.ReadFile(filepath.Join(filepath.Dir(path), fixture.Source))
		if err != nil {
			t.Fatal(err)
		}
		catalogue[fixture.Parser] = File{Content: content, Meta: fixture.Meta}
	}

	for _, registered := range Registered() {
		for owner, file := range catalogue {
			if owner == registered.Name {
				continue
			}
			if registered.Parser.Detect(file) {
				t.Errorf("parser %q claimed the fixture owned by %q", registered.Name, owner)
			}
		}
	}
}

func conformanceFixturePaths(t *testing.T) []string {
	t.Helper()
	fixtures, err := filepath.Glob(filepath.Join("testdata", "conformance", "*", "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("the parser conformance catalogue is empty")
	}
	return fixtures
}

func readConformanceFixture(t *testing.T, path string) conformanceFixture {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture conformanceFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func conformanceProjection(records Records) conformanceWant {
	got := conformanceWant{
		Sessions: make([]conformanceSession, 0, len(records.Sessions)),
		Memories: make([]conformanceMemory, 0, len(records.Memories)),
	}
	for _, session := range records.Sessions {
		projected := conformanceSession{
			ID: session.ID, SourceAgent: session.SourceAgent, Project: session.Project,
			Title: session.Title, ParentID: session.ParentID,
			OrphanedTools: projectConformanceTools(session.OrphanedTools),
			Exchanges:     make([]conformanceExchange, 0, len(session.Exchanges)),
		}
		for _, exchange := range session.Exchanges {
			projected.Exchanges = append(projected.Exchanges, conformanceExchange{
				HumanText: exchange.HumanText, AgentText: exchange.AgentText,
				Tools: projectConformanceTools(exchange.Tools),
			})
		}
		got.Sessions = append(got.Sessions, projected)
	}
	for _, memory := range records.Memories {
		got.Memories = append(got.Memories, conformanceMemory{
			Layer: memory.Layer, Content: memory.Content,
			SourceAgent: memory.SourceAgent, Project: memory.Project,
		})
	}
	return got
}

func projectConformanceTools(tools []ToolUse) []conformanceTool {
	if tools == nil {
		return nil
	}
	projected := make([]conformanceTool, 0, len(tools))
	for _, tool := range tools {
		projected = append(projected, conformanceTool{
			CallID: tool.CallID, Name: tool.Name, ParamsSummary: tool.ParamsSummary,
			HadError: tool.HadError, ErrorMessage: tool.ErrorMessage,
			InitiativeType: tool.InitiativeType,
		})
	}
	return projected
}

func TestConformanceProjectionExposesToolTelemetry(t *testing.T) {
	records := Records{Sessions: []Session{
		{ID: "nil", Exchanges: []Exchange{{}}},
		{ID: "empty", OrphanedTools: []ToolUse{},
			Exchanges: []Exchange{{Tools: []ToolUse{}}}},
		{ID: "populated", OrphanedTools: []ToolUse{{
			CallID: "orphan-call", Name: "orphan", ParamsSummary: "session params", HadError: true,
			ErrorMessage: "session failure", InitiativeType: "proactive",
		}}, Exchanges: []Exchange{{Tools: []ToolUse{{
			CallID: "attached-call", Name: "attached", ParamsSummary: "exchange params", HadError: true,
			ErrorMessage: "exchange failure", InitiativeType: "reactive",
		}}}}},
	}}

	got := conformanceProjection(records)
	if got.Sessions[0].OrphanedTools != nil || got.Sessions[0].Exchanges[0].Tools != nil {
		t.Fatalf("nil tools changed shape: %+v", got.Sessions[0])
	}
	if got.Sessions[1].OrphanedTools == nil || got.Sessions[1].Exchanges[0].Tools == nil {
		t.Fatalf("empty tools changed shape: %+v", got.Sessions[1])
	}
	wantOrphan := conformanceTool{CallID: "orphan-call", Name: "orphan", ParamsSummary: "session params",
		HadError: true, ErrorMessage: "session failure", InitiativeType: "proactive"}
	wantAttached := conformanceTool{CallID: "attached-call", Name: "attached", ParamsSummary: "exchange params",
		HadError: true, ErrorMessage: "exchange failure", InitiativeType: "reactive"}
	if !reflect.DeepEqual(got.Sessions[2].OrphanedTools, []conformanceTool{wantOrphan}) ||
		!reflect.DeepEqual(got.Sessions[2].Exchanges[0].Tools, []conformanceTool{wantAttached}) {
		t.Fatalf("projected tools = %+v", got.Sessions[2])
	}
}
