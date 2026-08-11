package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
			unavailable("codex", "there is no Codex session", "log in with `roca login codex`"),
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

// The credential never appears in any output. Presence is reported,
// the value never is.
func TestDoctorReportsTheCredentialsPresenceAndNeverItsValue(t *testing.T) {
	compatible, err := provider.NewOpenAICompatible(provider.OpenAIConfig{
		Name: "deepseek", BaseURL: "http://127.0.0.1:1/v1", APIKey: "sk-do-not-print-me",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	svc := seededServiceWith(t, provider.Cascade{
		Providers: []provider.Provider{compatible}, Probe: 200 * time.Millisecond,
	})

	report, err := svc.Doctor(context.Background())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if report.Providers[0].Credential != service.CredentialPresent {
		t.Fatalf("credential %q", report.Providers[0].Credential)
	}

	rendered, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(rendered), "sk-do-not-print-me") {
		t.Fatalf("the credential leaked into the report: %s", rendered)
	}
}

func TestDoctorDistinguishesWorkingPresentButFailedAndAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer working" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		http.Error(w, "dead", http.StatusUnauthorized)
	}))
	defer server.Close()
	providers := make([]provider.Provider, 0, 3)
	for _, tc := range []struct{ name, key string }{{"working", "working"}, {"dead", "dead"}, {"absent", ""}} {
		p, err := provider.NewOpenAICompatible(provider.OpenAIConfig{
			Name: tc.name, BaseURL: server.URL, Model: "m", APIKey: tc.key,
		})
		if err != nil {
			t.Fatal(err)
		}
		providers = append(providers, p)
	}
	svc := seededServiceWith(t, provider.Cascade{Providers: providers, Probe: time.Second})
	report, err := svc.Doctor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		ready      bool
		credential string
	}{{true, service.CredentialPresent}, {false, service.CredentialPresent}, {false, service.CredentialAbsent}}
	for i, verdict := range want {
		if report.Providers[i].Ready != verdict.ready || report.Providers[i].Credential != verdict.credential {
			t.Fatalf("provider %d = %+v, want ready=%v credential=%q", i, report.Providers[i], verdict.ready, verdict.credential)
		}
	}
	if report.Providers[1].Reason != "dead received HTTP status 401" {
		t.Fatalf("dead cause = %q", report.Providers[1].Reason)
	}
}

func TestDoctorSaysWhenTheLocalFloorNeedsNoCredential(t *testing.T) {
	svc := seededServiceWith(t, provider.Cascade{
		Providers: []provider.Provider{provider.NewOllama(provider.OllamaConfig{
			BaseURL: "http://127.0.0.1:1",
		})},
		Probe: 200 * time.Millisecond,
	})

	report, err := svc.Doctor(context.Background())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if report.Providers[0].Credential != service.CredentialNotNeeded {
		t.Fatalf("credential %q", report.Providers[0].Credential)
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
	svc := seededServiceWith(t, main, cascadeOf(
		unavailable("ollama", "it does not answer", "start it"),
		answering("mycorp", "")))

	report, err := svc.Doctor(context.Background())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if report.Titular != "codex" || report.InterpretTitular != "mycorp" {
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
	if len(together.Interpreters) != 0 || together.InterpretTitular != "" {
		t.Fatalf("an installation without the split reported one: %+v", together)
	}
}
