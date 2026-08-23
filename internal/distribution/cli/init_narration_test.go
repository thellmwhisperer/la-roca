package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

func TestInitNarratesItsPhasesAndPointsToThePromptLast(t *testing.T) {
	home := hermeticHome(t)

	out := runRoot(t, Build{Version: "test", Commit: "test-sha"},
		"init", "--db-path", filepath.Join(home, ".roca", "roca.db"))
	for _, want := range []string{
		"setup:",
		"agents: checking known sources",
		"agents detected:",
		"agents not found:",
		"database: inspecting",
		"database outcome: created",
		"rows: memories=",
		"ingest:",
		"delta:",
		"index: full-text index ready",
		"model:",
		"total:",
		"next steps:",
		"data directory:",
		"configuration:",
		"agent prompt:",
		"Paste its contents into the agent instructions you choose.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("init narration does not carry %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "## La Roca — local semantic memory") ||
		strings.Contains(out, "La Roca never edits agent instruction files") {
		t.Errorf("init dumped prompt.md instead of pointing to it:\n%s", out)
	}
}

func TestSemanticConsentConsumesStructuredWorkerResult(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("ROCA_VECTOR_STATE_DIR", "")
	root := t.TempDir()
	fixture := "#!/bin/sh\nprintf '%s\\n' '{\"background\":true,\"already_running\":true,\"pid\":4242,\"log_path\":\"/private/operator/path\"}'\nprintf '%s\\n' 'raw worker detail' >&2\n"
	installVectorFixture(t, root, fixture)
	var out, errOut bytes.Buffer
	env := &cliEnv{out: &out, errOut: &errOut, bundledVectorPayload: []byte(fixture)}
	paths := config.Paths{Home: root, Config: filepath.Join(root, "config.toml")}
	input := bufio.NewReader(strings.NewReader("yes\n"))
	if err := env.offerSemanticSearch(context.Background(), input, true, paths, true, readyProof()); err != nil {
		t.Fatal(err)
	}
	combined := out.String() + errOut.String()
	if !strings.Contains(combined, "setup continues in the background") {
		t.Fatalf("semantic setup output = %q", combined)
	}
	for _, detail := range []string{"4242", "/private/operator/path", "raw worker detail"} {
		if strings.Contains(combined, detail) {
			t.Fatalf("semantic setup output leaked %q: %q", detail, combined)
		}
	}
}

func TestSemanticConsentKeepsCompanionProgressOffTheInitSurface(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("ROCA_VECTOR_STATE_DIR", "")
	root := t.TempDir()
	fixture := "#!/bin/sh\ncase \" $* \" in *\" --stream-progress \"*) exit 9;; esac\n" +
		"printf '%s\\n' 'semantic index: 1/2 chunks · 1 added' >&2\n" +
		"printf '%s\\n' '{\"background\":true}'\n"
	installVectorFixture(t, root, fixture)
	progressPath := filepath.Join(root, "progress")
	progress, err := os.OpenFile(progressPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer progress.Close()
	var out bytes.Buffer
	env := &cliEnv{out: &out, errOut: progress, bundledVectorPayload: []byte(fixture)}
	paths := config.Paths{Home: root, Config: filepath.Join(root, "config.toml")}
	if err := env.offerSemanticSearch(context.Background(), bufio.NewReader(strings.NewReader("yes\n")),
		true, paths, true, readyProof()); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(progressPath)
	if strings.Contains(string(body), "chunks") || strings.Contains(string(body), "semantic index") {
		t.Fatalf("init streamed chunk counts to the terminal: %q", body)
	}
}

func TestSemanticConsentIsDurableForYesAndNo(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "declined", true: "enabled"}[enabled], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config.toml")
			body := "[features]\nvector_consent = true\nvector = false\n"
			if enabled {
				body = "[features]\nvector = true\n"
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			output := runSemanticConsent(t, path, "yes\n")
			if output != "" {
				t.Fatalf("decided consent prompted again: %q", output)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != body {
				t.Fatalf("decided consent changed: %q", after)
			}
		})
	}
}

func TestEnabledSemanticSearchDoesNotRestartDuringInit(t *testing.T) {
	root := t.TempDir()
	calls := filepath.Join(root, "calls")
	t.Setenv("ROCA_TEST_VECTOR_CALLS", calls)
	installVectorFixture(t, root, "#!/bin/sh\nprintf x >> \"$ROCA_TEST_VECTOR_CALLS\"\nprintf '%s\\n' '{\"background\":true}'\n")
	path := filepath.Join(root, "config.toml")
	body := "[features]\nvector_consent = true\nvector = true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		output := runSemanticConsent(t, path, "no\n")
		if strings.Contains(output, "[yes/no]") {
			t.Fatalf("durable consent prompted again: %q", output)
		}
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("semantic setup restarted with an enabled feature: %v", err)
	}
	if got := string(mustRead(t, path)); got != body {
		t.Fatalf("durable consent changed: %q", got)
	}
}

func TestSemanticDeclinePersistsExplicitDecision(t *testing.T) {
	root := t.TempDir()
	installVectorFixture(t, root, "#!/bin/sh\nexit 9\n")
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte("[features]\nplugins = true\nvector = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runSemanticConsent(t, path, "no\n")
	loaded, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Features.Vector {
		t.Fatal("declined semantic search remained enabled")
	}
	decided, err := config.HasValue(string(mustRead(t, path)), "features", "vector_consent")
	if err != nil || !decided {
		t.Fatalf("decline decision was not durable: decided=%v err=%v", decided, err)
	}
}

func TestSemanticConsentReasksInvalidAnswersAndLeavesEOFUndecided(t *testing.T) {
	root := t.TempDir()
	installVectorFixture(t, root, "#!/bin/sh\nprintf '%s\\n' '{\"background\":true}'\n")
	path := filepath.Join(root, "config.toml")
	before := "[features]\nvector = false\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	output := runSemanticConsent(t, path, "later\ny\n")
	if strings.Count(output, "[yes/no]") != 2 {
		t.Fatalf("invalid answer was not re-asked: %q", output)
	}
	loaded, err := config.LoadFile(path)
	if err != nil || !loaded.Features.Vector || !loaded.Features.VectorConsent {
		t.Fatalf("valid retry was not persisted: features=%+v err=%v", loaded.Features, err)
	}

	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	runSemanticConsent(t, path, "")
	if got := string(mustRead(t, path)); got != before {
		t.Fatalf("EOF changed the undecided configuration: %q", got)
	}
}

func TestSemanticSetupFailuresKeepConfigurationAndInitSuccessful(t *testing.T) {
	for _, testCase := range []struct {
		name, fixture string
	}{
		{name: "launch failure", fixture: "#!/bin/sh\nexit 7\n"},
		{name: "invalid acknowledgement", fixture: "#!/bin/sh\nprintf '%s\\n' '{\"background\":false}'\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			managed := filepath.Join(root, "managed-bin")
			t.Setenv("ROCA_PREFIX", managed)
			installVectorFixture(t, root, testCase.fixture)
			path := filepath.Join(root, "config.toml")
			t.Setenv("ROCA_CONFIG", path)
			before := "# operator setting\n[features]\nplugins = true\nvector = false\n"
			if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
				t.Fatal(err)
			}
			output := runSemanticConsent(t, path, "yes\n")
			if got := string(mustRead(t, path)); got != before {
				t.Fatalf("failed setup changed configuration: %q", got)
			}
			if strings.Count(output, "next step:") != 1 || !strings.Contains(output, "word search keeps answering") {
				t.Fatalf("failed setup output = %q", output)
			}
			if !strings.Contains(output, "`roca vector install`") {
				t.Fatalf("failed setup did not name the installed recovery command: %q", output)
			}
			if err := os.WriteFile(filepath.Join(managed, "roca-vector"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			var recoveryOutput bytes.Buffer
			code, err := executeWithOptions(&cliEnv{out: &recoveryOutput, errOut: &recoveryOutput},
				[]string{"vector", "install"}, nil, true)
			if code != ExitOK || err != nil {
				t.Fatalf("printed recovery command was not accepted: code=%d err=%v output=%q",
					code, err, recoveryOutput.String())
			}
		})
	}
}

func TestSemanticCompanionPlacementFailureKeepsConfigurationAndInitSuccessful(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("ROCA_VECTOR_STATE_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	prefix := filepath.Join(home, "not-a-directory")
	if err := os.WriteFile(prefix, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCA_PREFIX", prefix)
	t.Setenv("PATH", "")
	paths, err := config.Resolve(config.Input{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Config), 0o700); err != nil {
		t.Fatal(err)
	}
	before := "[features]\nvector = false\n"
	if err := os.WriteFile(paths.Config, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	env := &cliEnv{build: Build{Version: "test"}, out: &output, errOut: &output,
		bundledVectorPayload: []byte("#!/bin/sh\nexit 0\n")}
	if err := env.offerSemanticSearch(context.Background(), bufio.NewReader(strings.NewReader("yes\n")),
		true, paths, true, readyProof()); err != nil {
		t.Fatal(err)
	}
	if got := string(mustRead(t, paths.Config)); got != before {
		t.Fatalf("placement failure changed configuration: %q", got)
	}
	if strings.Count(output.String(), "next step:") != 1 {
		t.Fatalf("placement failure output = %q", output.String())
	}
	if !strings.Contains(output.String(), "`roca init`") {
		t.Fatalf("placement failure did not name init as its recovery: %q", output.String())
	}
	if _, err := runInitChoice(t, true, "new\n", "init"); err != nil {
		t.Fatalf("printed recovery command was not accepted in the preserved state: %v", err)
	}
}

func TestMissingBundledCompanionNeverExecutesAPathFallback(t *testing.T) {
	t.Setenv("CI", "")
	root := t.TempDir()
	calls := filepath.Join(root, "calls")
	t.Setenv("ROCA_TEST_VECTOR_CALLS", calls)
	installVectorFixture(t, root, "#!/bin/sh\nprintf x >> \"$ROCA_TEST_VECTOR_CALLS\"\n")
	path := filepath.Join(root, "config.toml")
	before := "[features]\nvector = false\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	env := &cliEnv{out: &output, errOut: &output}
	if err := env.offerSemanticSearch(context.Background(), bufio.NewReader(strings.NewReader("yes\n")),
		true, config.Paths{Home: root, Config: path}, true, readyProof()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("eligible init executed an unverified PATH companion: %v", err)
	}
	if got := string(mustRead(t, path)); got != before {
		t.Fatalf("missing payload changed configuration: %q", got)
	}
	if !strings.Contains(output.String(), "`roca vector install`") || strings.Count(output.String(), "next step:") != 1 {
		t.Fatalf("missing payload recovery output = %q", output.String())
	}
}

func TestIneligibleInitNeverOffersOrPlacesSemanticSearch(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		interactive bool
		explicit    bool
		ci          bool
		proof       *search.Proof
		config      string
		active      bool
	}{
		{name: "non-interactive", proof: readyProof()},
		{name: "explicit database", interactive: true, explicit: true, proof: readyProof()},
		{name: "CI", interactive: true, ci: true, proof: readyProof()},
		{name: "empty history", interactive: true, proof: &search.Proof{Empty: true}},
		{name: "ready without a hit", interactive: true, proof: &search.Proof{Ready: true}},
		{name: "feature enabled", interactive: true, proof: readyProof(), config: "[features]\nvector = true\n"},
		{name: "worker active", interactive: true, proof: readyProof(), active: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", "")
			t.Setenv("CI", map[bool]string{true: "true"}[testCase.ci])
			t.Setenv("ROCA_VECTOR_STATE_DIR", "")
			paths, err := config.Resolve(config.Input{Home: home})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(paths.Config), 0o700); err != nil {
				t.Fatal(err)
			}
			body := testCase.config
			if body == "" {
				body = "[features]\nvector = false\n"
			}
			if err := os.WriteFile(paths.Config, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if testCase.active {
				state := filepath.Join(pluginRoot(paths), "roca-vector", "state")
				if err := os.MkdirAll(state, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(state, ".worker"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			env := &cliEnv{out: &output, errOut: &output,
				bundledVectorPayload: []byte("synthetic payload")}
			if testCase.explicit {
				env.dbPath = filepath.Join(home, "explicit.db")
			}
			if err := env.offerSemanticSearch(context.Background(), bufio.NewReader(strings.NewReader("yes\n")),
				testCase.interactive, paths, true, testCase.proof); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "[yes/no]") {
				t.Fatalf("ineligible init asked for consent: %q", output.String())
			}
			if got := string(mustRead(t, paths.Config)); got != body {
				t.Fatalf("ineligible init changed config: %q", got)
			}
			if _, err := os.Stat(filepath.Join(pluginRoot(paths), "roca-vector", "plugin.json")); !os.IsNotExist(err) {
				t.Fatalf("ineligible init placed the companion: %v", err)
			}
		})
	}
}

func installVectorFixture(t *testing.T, root, fixture string) {
	t.Helper()
	t.Setenv("ROCA_VECTOR_STATE_DIR", "")
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "roca-vector"), []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func runSemanticConsent(t *testing.T, path, answer string) string {
	t.Helper()
	t.Setenv("CI", "")
	var out, errOut bytes.Buffer
	var payload []byte
	if fixture, found := resolveCompanion("vector", ""); found {
		payload, _ = os.ReadFile(fixture)
	}
	env := &cliEnv{out: &out, errOut: &errOut, bundledVectorPayload: payload}
	if err := env.offerSemanticSearch(context.Background(), bufio.NewReader(strings.NewReader(answer)),
		true, config.Paths{Home: filepath.Dir(path), Config: path}, false, readyProof()); err != nil {
		t.Fatal(err)
	}
	return out.String() + errOut.String()
}

func readyProof() *search.Proof {
	return &search.Proof{Ready: true, Word: "history", Matches: 1}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
