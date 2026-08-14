package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

			err := env.modelSetContext(context.Background(), nil, "codex", test.model)
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

// `model set` with no provider named targets the same provider `model check`
// probes: the first one the live cascade builds. Reading models.order straight
// out of the file made the two disagree whenever the environment set the order.
func TestModelSetOneArgumentTargetsTheFirstConfiguredProvider(t *testing.T) {
	for _, test := range []struct{ name, envOrder, want string }{
		{name: "configured order", want: "codex"},
		{name: "environment order wins", envOrder: "claude", want: "claude"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := isolatedLoginHome(t)
			path := modelConfigPath(home)
			writeFile(t, path, "[models]\norder = [\"codex\", \"ollama\"]\n")
			if test.envOrder != "" {
				t.Setenv("ROCA_MODELS_ORDER", test.envOrder)
			}
			fake := &fakePickerProvider{models: []string{"grok-green"}}
			env := validationEnv(t, fake)
			root := rootCommand(env)
			root.SetArgs([]string{"model", "set", "grok-green"})
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			file, err := config.LoadFile(path)
			if err != nil || file.Models.Providers[test.want].Model != "grok-green" {
				t.Fatalf("file=%+v err=%v", file, err)
			}
		})
	}
}

func TestModelCheckAndLoginAliasProbeWithoutWriting(t *testing.T) {
	for _, test := range []struct {
		name     string
		command  []string
		probeErr error
		wantErr  string
	}{
		{name: "model check", command: []string{"model", "check", "codex"}},
		{name: "login alias", command: []string{"login", "codex"}},
		{
			name: "rejected probe", command: []string{"model", "check", "codex"},
			probeErr: errors.New("account cannot reach it"),
			wantErr:  "codex model grok-green failed its account probe: account cannot reach it",
		},
		{
			name: "unknown provider", command: []string{"model", "check", "nosuch"},
			wantErr: `there is no provider "nosuch"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := isolatedLoginHome(t)
			path := modelConfigPath(home)
			before := "# preserve the operator's order\n[models]\norder = [\"ollama\", \"codex\"]\n\n[models.codex]\nmodel = \"grok-green\"\n"
			writeFile(t, path, before)
			fake := &fakePickerProvider{models: []string{"grok-green", "grok-other"}, probeErr: test.probeErr}
			env := validationEnv(t, fake)
			root := rootCommand(env)
			root.SetArgs(test.command)
			err := root.Execute()
			if test.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil || string(raw) != before {
				t.Fatalf("changed config: raw=%q err=%v", raw, readErr)
			}
			if test.wantErr == "" && (len(fake.probes) != 1 || fake.model != "grok-green") {
				t.Fatalf("probes=%v model=%q", fake.probes, fake.model)
			}
		})
	}
}

// An empty cascade is a configuration answer, not a failed probe: there is no
// session to reach, so `model check` says so and still succeeds. Which answer it
// is matters, though: an order that was turned off is not an order whose every
// entry this build had to drop, and reporting the second as the first denies the
// operator the file they wrote.
func TestModelCheckAnswersAnEmptyCascadeWithoutFailing(t *testing.T) {
	for _, test := range []struct {
		name, envOrder, file, wantReason, wantHuman string
	}{
		{
			name: "the order is turned off", envOrder: "none",
			wantReason: "no provider is declared",
			wantHuman:  "no provider is declared, so there is no model to probe",
		},
		{
			name:       "every declared provider was dropped",
			file:       "[models]\norder = [\"nosuch\"]\n",
			wantReason: "no declared provider can be used by this build",
			wantHuman:  `this version does not know the provider "nosuch"`,
		},
		{
			name:       "an empty order beside a warning about a provider it never named",
			file:       "[models]\norder = []\n\n[models.codex]\napi_key = \"synthetic-not-a-key\"\n",
			wantReason: "no provider is declared",
			wantHuman:  "no provider is declared, so there is no model to probe",
		},
	} {
		for _, machine := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%v", test.name, machine), func(t *testing.T) {
				home := isolatedLoginHome(t)
				if test.envOrder != "" {
					t.Setenv("ROCA_MODELS_ORDER", test.envOrder)
				}
				if test.file != "" {
					writeFile(t, modelConfigPath(home), test.file)
				}
				args := []string{"model", "check"}
				if machine {
					args = append(args, "--json")
				}
				out, err := runRootErr(t, Build{Version: "test"}, nil, args...)
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
				if !machine {
					if !strings.Contains(out, test.wantHuman) {
						t.Fatalf("human output = %q", out)
					}
					return
				}
				var result map[string]any
				if err := json.Unmarshal([]byte(out), &result); err != nil {
					t.Fatalf("%v: %s", err, out)
				}
				if result["ready"] != false || result["configuration_changed"] != false ||
					result["reason"] != test.wantReason {
					t.Fatalf("result = %+v", result)
				}
				warnings, _ := result["warnings"].([]any)
				if test.file != "" && len(warnings) == 0 {
					t.Fatalf("the dropped provider was not narrated: %+v", result)
				}
			})
		}
	}
}

// The empty cascade `model check` narrates is the state `model set` has to
// refuse: there is no provider to write a model for. It owes the same
// distinction between the two causes, the warnings that explain a drop, and an
// untouched configuration.
func TestModelSetRefusesAnEmptyCascadeWithoutWriting(t *testing.T) {
	for _, test := range []struct{ name, envOrder, file, wantErr string }{
		{
			name: "the order is turned off", envOrder: "none",
			wantErr: "there is no provider to set a model for: no provider is declared;",
		},
		{
			name: "every declared provider was dropped", file: "[models]\norder = [\"nosuch\"]\n",
			wantErr: "there is no provider to set a model for: no declared provider can be used by this build: " +
				`this version does not know the provider "nosuch"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := isolatedLoginHome(t)
			path := modelConfigPath(home)
			if test.envOrder != "" {
				t.Setenv("ROCA_MODELS_ORDER", test.envOrder)
			}
			if test.file != "" {
				writeFile(t, path, test.file)
			}
			out, err := runRootErr(t, Build{Version: "test"}, nil, "model", "set", "grok-green")
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q\n%s", err, test.wantErr, out)
			}
			raw, readErr := os.ReadFile(path)
			if test.file == "" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("the refusal wrote a configuration: raw=%q err=%v", raw, readErr)
				}
				return
			}
			if readErr != nil || string(raw) != test.file {
				t.Fatalf("the refusal changed the configuration: raw=%q err=%v", raw, readErr)
			}
		})
	}
}

// A provider the operator declared but this build cannot use owes the reason:
// the sibling `model set` path already carries the cascade's own explanation,
// and naming only the provider leaves nothing to act on.
func TestModelCheckNamesWhyADeclaredProviderCannotBeBuilt(t *testing.T) {
	home := isolatedLoginHome(t)
	writeFile(t, modelConfigPath(home), "[models]\norder = [\"nosuch\"]\n")
	out, err := runRootErr(t, Build{Version: "test"}, nil, "model", "check", "nosuch")
	if err == nil {
		t.Fatalf("unusable provider succeeded:\n%s", out)
	}
	for _, want := range []string{"nosuch", "does not know the provider"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error omits %q: %v", want, err)
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
