package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// Provider order comes from configuration, and doctor
// reports it in the declared order with a verdict per provider.
func TestDoctorReportsTheProvidersInTheDeclaredOrder(t *testing.T) {
	svc := seededServiceWith(t, provider.Cascade{
		Providers: []provider.Provider{
			unavailable("codex", "there is no Codex session", "verify it with `roca model check codex`"),
			answering("ollama", ""),
		},
		Probe: time.Second,
	})

	report, err := svc.Doctor(context.Background())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if len(report.Providers) != 2 {
		t.Fatalf("providers %+v", report.Providers)
	}
	if report.Providers[0].Name != "codex" || report.Providers[1].Name != "ollama" {
		t.Fatalf("the declared order was not respected: %+v", report.Providers)
	}
	if report.Providers[0].Ready {
		t.Fatal("the first one is not available and it says it is")
	}
	if report.Providers[0].Reason == "" || report.Providers[0].Action == "" {
		t.Fatalf("a diagnosis with no remedy: %+v", report.Providers[0])
	}
	if report.Titular != "ollama" {
		t.Fatalf("titular %q: it is the first available one", report.Titular)
	}
}

func TestDoctorReportsDetectedBinariesAndTheFactorySelection(t *testing.T) {
	local := answering("codex", "")
	local.commandTransport = true
	svc := seededServiceWith(t, provider.Cascade{
		Providers:        []provider.Provider{local, answering("ollama", "")},
		DetectedBinaries: []string{"codex"},
		FallbackDiagnostics: []provider.Attempt{{
			Name: "claude", Reason: "claude binary not found in PATH",
		}},
		FactoryDefault: true,
	})
	report, err := svc.Doctor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(report.DetectedModelBinaries, ",") != "codex" ||
		strings.Join(report.MissingModelBinaries, ",") != "claude" ||
		!report.FactoryDefault || report.FactoryDefaultProvider != "codex" {
		t.Fatalf("factory diagnosis = %+v", report)
	}
}

func TestDoctorPrescribesTheExactLayerRegistryRepair(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	if _, err := svc.DB().SQL().Exec(
		`INSERT INTO memories (layer, content, origin)
		 VALUES ('knowledge', 'synthetic drift', 'agent')`); err != nil {
		t.Fatal(err)
	}

	report, err := svc.Doctor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := "roca layers add 'knowledge'"
	if len(report.LayerRepairs) != 1 || report.LayerRepairs[0] != want {
		t.Fatalf("layer repairs = %v, want %q", report.LayerRepairs, want)
	}
}

func TestDoctorSaysSoWhenNobodyIsAvailable(t *testing.T) {
	svc := seededServiceWith(t, provider.Cascade{
		Providers: []provider.Provider{unavailable("ollama", "it does not answer", "start it")},
		Probe:     time.Second,
	})

	report, err := svc.Doctor(context.Background())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if report.Titular != "" {
		t.Fatalf("titular %q", report.Titular)
	}
}

func TestDoctorCarriesTheConfigurationsWarnings(t *testing.T) {
	svc := seededServiceWith(t, provider.Cascade{
		Warnings: []string{"this version does not know the provider \"telepathy\""},
	})

	report, err := svc.Doctor(context.Background())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "telepathy") {
		t.Fatalf("warnings %v", report.Warnings)
	}
}

func TestDoctorReportsTheOldestCorpusMomentAndItsProject(t *testing.T) {
	svc := seededServiceWith(t, provider.Cascade{})
	if _, err := svc.DB().SQL().Exec(`
		INSERT INTO sessions (session_id, project, started_at) VALUES
		('later', 'younger-project', '2026-02-02T10:00:00Z'),
		('first', 'bedrock-project', '2026-01-31T08:15:00Z');
		INSERT INTO exchanges (session_id, human_timestamp, agent_timestamp) VALUES
		('later', '2026-02-02T10:00:01Z', '2026-02-02T10:00:02Z');
		INSERT INTO memories (layer, content, origin, project, created_at) VALUES
		('project', 'younger memory', 'agent', 'younger-project', '2026-02-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	report, err := svc.Doctor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Bedrock.Timestamp != "2026-01-31T08:15:00Z" || report.Bedrock.Project != "bedrock-project" {
		t.Fatalf("bedrock = %+v", report.Bedrock)
	}
}

func TestDoctorNamesTheDatabaseAndTheConfigurationFile(t *testing.T) {
	svc := seededServiceWith(t, provider.Cascade{})

	report, err := svc.Doctor(context.Background())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if report.DBPath == "" {
		t.Error("it does not name the database")
	}
	if report.Version == "" || report.SourceSHA == "" {
		t.Errorf("it does not say which code it comes from: %+v", report)
	}
	if report.Memories == 0 {
		t.Error("it does not count what is in the memory")
	}
}

// The interpretation decision is reported exactly like the main one: every
// declared provider with its verdict, and the one that is going to read the
// rows. An installation that does not split the two inferences reports nothing
// extra, because nothing extra was decided.
func TestDoctorReportsTheInterpretationDecision(t *testing.T) {
	main := cascadeOf(answering("codex", ""))
	svc := initialized(t, freshPaths(t), func(options *service.Options) {
		options.Providers = main
		options.Interpreters = cascadeOf(
			unavailable("ollama", "it does not answer", "start it"),
			answering("mycorp", ""))
		options.Explorers = cascadeOf(answering("claude", ""))
	})

	report, err := svc.Doctor(context.Background())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if report.Titular != "codex" || report.InterpretTitular != "mycorp" ||
		report.ExploreTitular != "claude" || len(report.Explorers) != 1 {
		t.Fatalf("the two decisions were not told apart: %+v", report)
	}
	if len(report.Interpreters) != 2 || report.Interpreters[0].Reason == "" ||
		report.Interpreters[0].Action == "" {
		t.Fatalf("an interpretation diagnosis with no remedy: %+v", report.Interpreters)
	}

	together, err := seededServiceWith(t, main).Doctor(context.Background())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if len(together.Interpreters) != 0 || together.InterpretTitular != "" ||
		len(together.Explorers) != 0 || together.ExploreTitular != "" {
		t.Fatalf("an installation without the split reported one: %+v", together)
	}
}
