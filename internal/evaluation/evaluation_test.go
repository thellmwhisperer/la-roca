package evaluation

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestTheEmbeddedGoldenSetCarriesCoverageAndHeadroom(t *testing.T) {
	suite, err := LoadSuite()
	if err != nil {
		t.Fatalf("LoadSuite: %v", err)
	}
	if len(suite.Cases) < 15 || len(suite.Cases) > 25 {
		t.Fatalf("cases = %d; want 15..25", len(suite.Cases))
	}

	wanted := map[string]bool{
		"person": false, "history": false, "count": false,
		"time": false, "provenance": false,
	}
	headroom, rescued := 0, 0
	for _, golden := range suite.Cases {
		if golden.Question == "" || golden.ExpectedKind == "" || golden.ExpectedMarker == "" {
			t.Fatalf("case %q lacks the public golden contract: %+v", golden.ID, golden)
		}
		wanted[golden.Category] = true
		if golden.Headroom != "" {
			headroom++
		}
		if len(golden.RescuePath) > 0 {
			rescued++
		}
	}
	for category, found := range wanted {
		if !found {
			t.Errorf("golden set lacks %s coverage", category)
		}
	}
	if headroom < 3 || rescued == 0 {
		t.Fatalf("headroom=%d rescued=%d; want at least 3 and at least 1", headroom, rescued)
	}
}

func TestExternalGoldenSetLoadsReplayPlansFromItsPrivateSidecar(t *testing.T) {
	dir := t.TempDir()
	casesPath := filepath.Join(dir, "owner-cases.json")
	suite := Suite{SchemaVersion: 1, Fixture: "owner-private", Cases: []Case{{
		ID: "private-person", Category: "person", Question: "Who owns Quartz?",
		ExpectedKind: "row_contains", ExpectedMarker: "Ada owns Quartz",
	}}}
	raw, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(casesPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	broken := filepath.Join(dir, "broken-cases.json")
	if err := os.WriteFile(broken, []byte(`{"schema_version":1,"cases":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSuiteFile(broken); err == nil ||
		!strings.Contains(err.Error(), broken) || !strings.Contains(err.Error(), "fixture") {
		t.Fatalf("envelope error = %v; want the file and the missing key", err)
	}

	loaded, err := LoadSuiteFile(casesPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fixture != "owner-private" || len(loaded.Cases) != 1 || len(loaded.Plans) != 0 {
		t.Fatalf("external suite = %+v", loaded)
	}
	if err := ValidateReplay(loaded); err == nil || !strings.Contains(err.Error(), RecordedPlansPath(casesPath)) {
		t.Fatalf("missing replay sidecar error = %v", err)
	}
	if err := os.WriteFile(RecordedPlansPath(casesPath), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSuiteFile(casesPath); err != nil {
		t.Fatalf("live case loading consulted replay sidecar: %v", err)
	}
	plans := `{"provider":"recorded","model":"owner-v1","plans":[{"case_id":"private-person","sql":["SELECT content FROM memories LIMIT 5"]}]}`
	if err := os.WriteFile(RecordedPlansPath(casesPath), []byte(plans), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadSuiteFile(casesPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadReplayPlans(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateReplay(loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Provider != "recorded" || loaded.Model != "owner-v1" || len(loaded.Plans) != 1 {
		t.Fatalf("external replay labels/plans = %+v", loaded)
	}
}

func TestReplayMeasuresTheRecordedBaseline(t *testing.T) {
	ctx := context.Background()
	suite, err := LoadSuite()
	if err != nil {
		t.Fatalf("LoadSuite: %v", err)
	}
	svc := fixtureService(t, provider.Cascade{})

	report, err := Run(ctx, svc, suite, ReplayPlanner(suite), "replay")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Metrics.Cases != 20 || report.Metrics.Passed != 17 ||
		report.Metrics.HitAt1 != 14 || report.Metrics.HitAt5 != 17 {
		t.Fatalf("unexpected recorded baseline: %+v", report.Metrics)
	}
	if report.Metrics.ZeroResultQueries != 6 || report.Metrics.TotalQueries != 23 ||
		report.Metrics.RescueCases != 2 || report.Metrics.AnsweredRescueCases != 2 ||
		math.Abs(report.Metrics.QueriesToAnswer-2.5) > 0.001 {
		t.Fatalf("unexpected rescue metrics: %+v", report.Metrics)
	}
	for _, result := range report.Cases {
		if result.Headroom != "" && result.HitAt5 {
			t.Errorf("headroom case %s unexpectedly passed", result.ID)
		}
	}
}

func TestMarkersAreIDIndependent(t *testing.T) {
	rows := []map[string]any{
		{"id": int64(9081), "text": "Nora Vale approved the Aurora launch", "source_agent": "codex"},
		{"count": int64(3)},
	}
	tests := []struct {
		kind, marker string
		at1, at5     bool
	}{
		{"row_contains", "approved the Aurora launch", true, true},
		{"field_equals", "source_agent=codex", true, true},
		{"field_equals", "id=1", false, false},
		{"count_gt", "2", false, true},
		{"count_equals", "3", false, true},
	}
	for _, test := range tests {
		golden := Case{ExpectedKind: test.kind, ExpectedMarker: test.marker}
		at1, at5, err := Match(golden, rows)
		if err != nil {
			t.Errorf("%s/%s: %v", test.kind, test.marker, err)
		} else if at1 != test.at1 || at5 != test.at5 {
			t.Errorf("%s/%s = %v/%v; want %v/%v",
				test.kind, test.marker, at1, at5, test.at1, test.at5)
		}
	}
}

func TestReportsAreHumanMachineAndReleaseNoteReady(t *testing.T) {
	report := Report{
		Mode: "replay", Fixture: "synthetic-v1",
		Producers: []Producer{{Provider: "recorded", Model: "fixed-sql-v1", Plans: 20}},
		Metrics: Metrics{Cases: 20, Passed: 17, HitAt1: 16, HitAt5: 17,
			ZeroResultRate: 0.2609, RescueCases: 2, AnsweredRescueCases: 2,
			QueriesToAnswer: 2.5},
		Cases: []CaseResult{{ID: "person-approval", HitAt1: true, HitAt5: true}},
	}

	human := RenderHuman(report)
	markdown := RenderMarkdown(report)
	for _, marker := range []string{"hit@1", "hit@5", "zero-result rate", "queries-to-answer", "recorded/fixed-sql-v1"} {
		if !strings.Contains(human, marker) || !strings.Contains(markdown, marker) {
			t.Errorf("reports omit %q\nhuman:\n%s\nmarkdown:\n%s", marker, human, markdown)
		}
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if !strings.Contains(string(raw), `"hit_at_1"`) || !strings.Contains(string(raw), `"wall_ms"`) {
		t.Fatalf("machine report lacks stable fields: %s", raw)
	}
}

func TestArchivePreservesTheCompleteReportAndEveryFormat(t *testing.T) {
	report := Report{
		Mode: "live", Fixture: "synthetic-v1", LogPath: "/workspace/.tmp/eval/logs/eval-2026-08-12.jsonl",
		Producers: []Producer{{Provider: "codex", Model: "gpt-eval", Plans: 2}},
		Metrics:   Metrics{Cases: 2, Passed: 1},
		Cases: []CaseResult{
			{ID: "one", Question: "first", ExpectedKind: "row_contains", ExpectedMarker: "alpha",
				Attempts: []AttemptResult{{Question: "first", SQL: "SELECT 'alpha'", Provider: "codex", Model: "gpt-eval",
					Columns: []string{"answer"}, ResultRows: []map[string]any{{"answer": "alpha"}}}}},
			{ID: "two", Question: "second", ExpectedKind: "field_equals", ExpectedMarker: "model=gpt-eval"},
		},
	}
	stamp := time.Date(2026, 8, 12, 12, 34, 56, 0, time.UTC)
	archive, err := NewArchive(report, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if !archive.Timestamp.Equal(stamp) || archive.Mode != "live" ||
		len(archive.PlanProducers) != 1 || len(archive.Report.Cases) != 2 {
		t.Fatalf("archive lost run source data: %+v", archive)
	}
	for name, output := range map[string]string{
		"human": archive.Formats.Human, "markdown": archive.Formats.Markdown,
		"json": archive.Formats.JSON,
	} {
		for _, marker := range []string{"live", "codex", "gpt-eval", report.LogPath} {
			if !strings.Contains(output, marker) {
				t.Errorf("%s archive omits %q:\n%s", name, marker, output)
			}
		}
	}
	var machine Report
	if err := json.Unmarshal([]byte(archive.Formats.JSON), &machine); err != nil {
		t.Fatalf("archived JSON is not the complete machine report: %v", err)
	}
	if len(machine.Cases) != len(report.Cases) || machine.Cases[0].Attempts[0].SQL == "" ||
		len(machine.Cases[0].Attempts[0].ResultRows) != 1 {
		t.Fatalf("archived JSON lost cases or attempts: %+v", machine)
	}
}

func TestQueriesToAnswerNamesAnsweredAndDeclaredRescueCases(t *testing.T) {
	svc := fixtureService(t, provider.Cascade{})
	suite := Suite{Fixture: "synthetic-v1", Cases: []Case{
		{ID: "answered", Question: "first", ExpectedKind: "row_contains",
			ExpectedMarker: "Nora Vale", RescuePath: []string{"retry"}},
		{ID: "missed", Question: "first", ExpectedKind: "row_contains",
			ExpectedMarker: "absent marker", RescuePath: []string{"retry"}},
	}}
	planner := plannerFunc(func(_ context.Context, golden Case, attempt int, _ string) (Plan, error) {
		sql := "SELECT content AS text FROM memories WHERE project = 'missing' LIMIT 5"
		if golden.ID == "answered" && attempt == 1 {
			sql = "SELECT content AS text FROM memories WHERE project = 'aurora' AND content LIKE '%Nora Vale%' LIMIT 5"
		}
		return Plan{SQL: []string{sql}, Provider: "test", Model: "fixed"}, nil
	})
	report, err := Run(context.Background(), svc, suite, planner, "live")
	if err != nil {
		t.Fatal(err)
	}
	if report.Metrics.RescueCases != 2 || report.Metrics.AnsweredRescueCases != 1 ||
		report.Metrics.QueriesToAnswer != 2 {
		t.Fatalf("rescue metrics = %+v", report.Metrics)
	}
	if report.Metrics.HitAt1 != 0 || report.Cases[0].HitAt1 || !report.Cases[0].HitAt5 {
		t.Fatalf("a rescue attempt inflated hit@1: %+v", report.Cases[0])
	}
	for _, output := range []string{RenderHuman(report), RenderMarkdown(report)} {
		if !strings.Contains(output, "2.00") ||
			!strings.Contains(output, "1/2 answered rescue cases") {
			t.Fatalf("rescue denominator is unclear:\n%s", output)
		}
	}
	raw, err := json.Marshal(report)
	if err != nil || !strings.Contains(string(raw), `"answered_rescue_cases":1`) ||
		!strings.Contains(string(raw), `"rescue_cases":2`) {
		t.Fatalf("machine rescue denominator = %s, err=%v", raw, err)
	}
}

func TestMarkersAreFoundPastTheDisplayBudget(t *testing.T) {
	svc := fixtureService(t, provider.Cascade{})
	prefix := strings.Repeat("x", service.DefaultMaxChars+10)
	suite := Suite{Fixture: "synthetic-v1", Cases: []Case{{ID: "long-field",
		Question: "Who approved Aurora?", ExpectedKind: "row_contains",
		ExpectedMarker: "Nora Vale"}}}
	planner := plannerFunc(func(context.Context, Case, int, string) (Plan, error) {
		return Plan{SQL: []string{"SELECT '" + prefix +
			"' || content AS text FROM memories WHERE content LIKE '%Nora Vale%' LIMIT 5"},
			Provider: "test", Model: "fixed"}, nil
	})

	report, err := Run(context.Background(), svc, suite, planner, "replay")
	if err != nil {
		t.Fatal(err)
	}
	rows := report.Cases[0].Attempts[0].ResultRows
	if report.Metrics.HitAt1 != 1 || len(rows) == 0 ||
		!strings.Contains(rows[0]["text"].(string), "Nora Vale") {
		t.Fatalf("a marker past the display budget was scored as a miss: %+v", report.Cases[0])
	}
}

func TestLivePlansNameTheirProviderAndModel(t *testing.T) {
	ctx := context.Background()
	svc := fixtureService(t,
		provider.Cascade{Providers: []provider.Provider{fixedPlanProvider{}}})

	golden := Case{ID: "live", Question: "Who approved Aurora?",
		ExpectedKind: "row_contains", ExpectedMarker: "Nora Vale"}
	plan, err := LivePlanner(svc).Plan(ctx, golden, 0, golden.Question)
	if err != nil {
		t.Fatalf("live plan: %v", err)
	}
	if plan.Provider != "test-provider" || plan.Model != "planner-v2" || len(plan.SQL) != 1 {
		t.Fatalf("unlabelled live plan: %+v", plan)
	}
}

func fixtureService(t *testing.T, providers provider.Cascade) *service.Service {
	t.Helper()
	dbPath, cleanup, err := PrepareFixture(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("PrepareFixture: %v", err)
	}
	t.Cleanup(cleanup)
	svc, err := service.Open(service.Options{DBPath: dbPath, ReadOnly: true, Providers: providers})
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

type fixedPlanProvider struct{}

func (fixedPlanProvider) Name() string    { return "test-provider" }
func (fixedPlanProvider) ModelID() string { return "planner-v2" }
func (fixedPlanProvider) Ready(context.Context) provider.Readiness {
	return provider.Readiness{Ready: true, ModelID: "planner-v2"}
}
func (fixedPlanProvider) Models(context.Context) provider.ModelReport {
	return provider.ModelReport{Ready: true, Models: []string{"planner-v2"}}
}
func (fixedPlanProvider) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{
		Content:  "SELECT content AS text FROM memories WHERE project = 'aurora' ORDER BY id LIMIT 5",
		Provider: "test-provider", ModelID: "planner-v2",
	}, nil
}
