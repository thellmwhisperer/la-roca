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
	ID          string                `json:"id"`
	SourceAgent string                `json:"source_agent"`
	Project     string                `json:"project,omitempty"`
	Title       string                `json:"title,omitempty"`
	ParentID    string                `json:"parent_id,omitempty"`
	Exchanges   []conformanceExchange `json:"exchanges"`
}

type conformanceExchange struct {
	HumanText string `json:"human_text"`
	AgentText string `json:"agent_text"`
}

type conformanceMemory struct {
	Layer       string `json:"layer"`
	Content     string `json:"content"`
	SourceAgent string `json:"source_agent"`
	Project     string `json:"project,omitempty"`
}

// TestRegisteredParsersConform is the contribution harness. Every directory in
// testdata/conformance is a complete, synthetic worked example: its manifest
// names a registered parser, its own source file, the declared destination and
// the normalized records that must result.
func TestRegisteredParsersConform(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "conformance", "*", "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("the parser conformance catalogue is empty")
	}

	covered := map[string]bool{}
	registeredNames := map[string]bool{}
	for _, registered := range Registered() {
		if registered.Name == "" || registered.Parser == nil {
			t.Fatalf("incomplete parser registration: %+v", registered)
		}
		if registeredNames[registered.Name] {
			t.Fatalf("parser %q is registered more than once", registered.Name)
		}
		registeredNames[registered.Name] = true
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

func TestDestinationsRejectCrossSurfaceRecords(t *testing.T) {
	corpus := Records{Sessions: []Session{{ID: "synthetic-session"}}}
	store := Records{Memories: []Memory{{Content: "synthetic memory"}}}
	for _, testCase := range []struct {
		name        string
		destination Destination
		records     Records
		wantError   bool
	}{
		{"corpus to corpus", DestinationCorpus, corpus, false},
		{"store to corpus", DestinationCorpus, store, true},
		{"store to store", DestinationStore, store, false},
		{"corpus to store", DestinationStore, corpus, true},
		{"corpus to both", DestinationBoth, corpus, false},
		{"store to both", DestinationBoth, store, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.destination.Conforms(testCase.records)
			if (err != nil) != testCase.wantError {
				t.Fatalf("Conforms() error = %v, want error %t", err, testCase.wantError)
			}
		})
	}
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
			Exchanges: make([]conformanceExchange, 0, len(session.Exchanges)),
		}
		for _, exchange := range session.Exchanges {
			projected.Exchanges = append(projected.Exchanges, conformanceExchange{
				HumanText: exchange.HumanText, AgentText: exchange.AgentText,
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
