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

func TestModelCheckAndLoginAliasProbeWithoutWriting(t *testing.T) {
	for _, command := range [][]string{{"model", "check", "codex"}, {"login", "codex"}} {
		home := isolatedLoginHome(t)
		path := modelConfigPath(home)
		before := "# preserve the operator's order\n[models]\norder = [\"ollama\", \"codex\"]\n\n[models.codex]\nmodel = \"grok-green\"\n"
		writeFile(t, path, before)
		fake := &fakePickerProvider{models: []string{"grok-green", "grok-other"}}
		env := validationEnv(t, fake)
		root := rootCommand(env)
		root.SetArgs(command)
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v", command, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != before {
			t.Fatalf("%v changed config: raw=%q err=%v", command, raw, err)
		}
		if len(fake.probes) != 1 || fake.model != "grok-green" {
			t.Fatalf("%v probes=%v model=%q", command, fake.probes, fake.model)
		}
	}
}

func TestModelSetWithoutAnIDPicksFromTheTargetCatalogue(t *testing.T) {
	for _, test := range []struct {
		name, provider string
		args           []string
	}{
		{name: "first configured provider", provider: "codex", args: []string{"model", "set"}},
		{name: "named provider", provider: "claude", args: []string{"model", "set", "claude"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := isolatedLoginHome(t)
			path := modelConfigPath(home)
			writeFile(t, path, "[models]\norder = [\"codex\", \"claude\"]\n\n[models.codex]\nmodel = \"grok-green\"\n\n[models.claude]\nmodel = \"grok-green\"\n")
			fake := &fakePickerProvider{models: []string{"grok-green", "grok-other"}}
			env := validationEnv(t, fake)
			env.modelPicker = fixedModelPicker("grok-other")
			root := rootCommand(env)
			root.SetArgs(test.args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			file, err := config.LoadFile(path)
			if err != nil || file.Models.Providers[test.provider].Model != "grok-other" {
				t.Fatalf("file=%+v err=%v", file, err)
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
