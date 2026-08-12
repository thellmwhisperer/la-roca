package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestModelSetNeverWritesBeforeCatalogueAndProbePass(t *testing.T) {
	for _, test := range []struct {
		name       string
		model      string
		probeErr   error
		wantErr    string
		wantWrites bool
	}{
		{name: "unknown ID", model: "luna", wantErr: `model "luna" is not in codex's catalogue`},
		{name: "account rejection", model: "grok-green", probeErr: errors.New(`server said: model is not enabled`), wantErr: "server said: model is not enabled"},
		{name: "green probe", model: "grok-green", wantWrites: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := isolatedLoginHome(t)
			path := modelConfigPath(home)
			before := "# untouched\n[models.codex]\nmodel = \"grok-old\"\n"
			writeFile(t, path, before)
			fake := &fakePickerProvider{models: []string{"grok-green", "grok-other"}, probeErr: test.probeErr}
			env := validationEnv(t, fake)

			err := env.modelSetContext(context.Background(), "codex", test.model)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if test.wantWrites {
				if !strings.Contains(string(raw), `model = "grok-green"`) || len(fake.probes) != 1 {
					t.Fatalf("config=%s probes=%v", raw, fake.probes)
				}
				return
			}
			if string(raw) != before {
				t.Fatalf("failed validation changed config:\n--- want ---\n%s--- got ---\n%s", before, raw)
			}
			if test.model == "luna" && len(fake.probes) != 0 {
				t.Fatalf("unknown catalogue ID was probed: %v", fake.probes)
			}
		})
	}
}

func TestModelSetOneArgumentTargetsTheFirstConfiguredProvider(t *testing.T) {
	home := isolatedLoginHome(t)
	path := modelConfigPath(home)
	writeFile(t, path, "[models]\norder = [\"codex\", \"ollama\"]\n")
	fake := &fakePickerProvider{models: []string{"grok-green"}}
	env := validationEnv(t, fake)
	root := rootCommand(env)
	root.SetArgs([]string{"model", "set", "grok-green"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	file, err := config.LoadFile(path)
	if err != nil || file.Models.Providers["codex"].Model != "grok-green" {
		t.Fatalf("file=%+v err=%v", file, err)
	}
}

func TestLoginPickerPersistsOnlyAfterItsProbe(t *testing.T) {
	for _, test := range []struct {
		name       string
		requested  string
		picked     string
		probeErr   error
		wantModel  string
		wantErr    string
		wantProbes int
	}{
		{name: "arrow choice", picked: "grok-other", wantModel: "grok-other", wantProbes: 1},
		{name: "free text flag", requested: "luna", wantErr: "not in codex's catalogue"},
		{name: "probe rejected", requested: "grok-green", probeErr: errors.New("account cannot reach it"), wantErr: "account cannot reach it", wantProbes: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := isolatedLoginHome(t)
			path := modelConfigPath(home)
			before := "# login must preserve me\n"
			writeFile(t, path, before)
			fake := &fakePickerProvider{models: []string{"grok-green", "grok-other"}, probeErr: test.probeErr}
			env := validationEnv(t, fake)
			if test.picked != "" {
				env.modelPicker = fixedModelPicker(test.picked)
			}
			root := rootCommand(env)
			args := []string{"login", "codex"}
			if test.requested != "" {
				args = append(args, "--model", test.requested)
			}
			root.SetArgs(args)
			err := root.Execute()
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
				raw, readErr := os.ReadFile(path)
				if readErr != nil || string(raw) != before {
					t.Fatalf("failed login changed config: raw=%q err=%v", raw, readErr)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				file, loadErr := config.LoadFile(path)
				if loadErr != nil || file.Models.Providers["codex"].Model != test.wantModel {
					t.Fatalf("persisted file=%+v err=%v", file, loadErr)
				}
			}
			if len(fake.probes) != test.wantProbes {
				t.Fatalf("probes = %d, want %d", len(fake.probes), test.wantProbes)
			}
		})
	}
}

func TestArrowModelPickerSelectsOnlyAListedID(t *testing.T) {
	var output strings.Builder
	chosen, err := readArrowModelChoice(strings.NewReader("\x1b[B\r"), &output,
		[]string{"gpt-first", "gpt-second"}, "gpt-first")
	if err != nil {
		t.Fatal(err)
	}
	if chosen != "gpt-second" {
		t.Fatalf("chosen = %q, want gpt-second", chosen)
	}
	if !strings.Contains(output.String(), "Use ↑/↓") {
		t.Fatalf("picker does not explain its controls: %q", output.String())
	}
}

type fakePickerProvider struct {
	model    string
	models   []string
	probeErr error
	probes   []provider.ChatRequest
}

func (p *fakePickerProvider) Name() string    { return provider.NameCodex }
func (p *fakePickerProvider) ModelID() string { return p.model }
func (p *fakePickerProvider) Ready(context.Context) provider.Readiness {
	return provider.Readiness{Ready: true, ModelID: p.model}
}
func (p *fakePickerProvider) Models(context.Context) provider.ModelReport {
	return provider.ModelReport{Ready: true, Models: slices.Clone(p.models)}
}
func (p *fakePickerProvider) Chat(_ context.Context, request provider.ChatRequest) (provider.ChatResponse, error) {
	p.probes = append(p.probes, request)
	return provider.ChatResponse{}, p.probeErr
}

func validationEnv(t *testing.T, fake *fakePickerProvider) *cliEnv {
	t.Helper()
	var output strings.Builder
	return &cliEnv{
		build: Build{Version: "test"}, out: &output, errOut: &output,
		modelBackend: &providerModelBackend{
			build: func(_ string, model string) (provider.Provider, error) {
				fake.model = model
				return fake, nil
			},
		},
	}
}

func fixedModelPicker(model string) modelPicker {
	return func(io.Reader, io.Writer, []string, string) (string, error) { return model, nil }
}
