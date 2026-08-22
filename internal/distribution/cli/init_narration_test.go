package cli

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
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
	root := t.TempDir()
	fixture := "#!/bin/sh\nprintf '%s\\n' '{\"background\":true,\"already_running\":true,\"pid\":4242,\"log_path\":\"/private/operator/path\"}'\nprintf '%s\\n' 'raw worker detail' >&2\n"
	installVectorFixture(t, root, fixture)
	var out, errOut bytes.Buffer
	env := &cliEnv{out: &out, errOut: &errOut, dbPath: filepath.Join(root, "roca.db")}
	paths := config.Paths{Config: filepath.Join(root, "config.toml")}
	input := bufio.NewReader(strings.NewReader("yes\n"))
	if err := env.offerSemanticSearch(context.Background(), input, true, paths, true); err != nil {
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

func TestSemanticConsentLeavesLiveProgressConnectedAfterInitReturns(t *testing.T) {
	root := t.TempDir()
	fixture := "#!/bin/sh\ncase \" $* \" in *\" --stream-progress \"*) ;; *) exit 9;; esac\n" +
		"(sleep 0.1; printf '%s\\n' 'downloading the embedding model · 1/2' >&2) &\n" +
		"printf '%s\\n' '{\"background\":true}'\n"
	installVectorFixture(t, root, fixture)
	progressPath := filepath.Join(root, "progress")
	progress, err := os.OpenFile(progressPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer progress.Close()
	var out bytes.Buffer
	env := &cliEnv{out: &out, errOut: progress, dbPath: filepath.Join(root, "roca.db")}
	paths := config.Paths{Config: filepath.Join(root, "config.toml")}
	if err := env.offerSemanticSearch(context.Background(), bufio.NewReader(strings.NewReader("yes\n")),
		true, paths, true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		body, _ := os.ReadFile(progressPath)
		if strings.Contains(string(body), "downloading the embedding model · 1/2") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("live semantic setup progress stopped when init returned")
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

func TestSemanticConsentRetriesSetupWithoutPromptingAgain(t *testing.T) {
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
	if got := string(mustRead(t, calls)); got != "xx" {
		t.Fatalf("semantic setup launches = %q, want one per init", got)
	}
	if got := string(mustRead(t, path)); got != body {
		t.Fatalf("durable consent changed: %q", got)
	}
}

func TestSemanticDeclinePersistsExplicitDecision(t *testing.T) {
	root := t.TempDir()
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

func installVectorFixture(t *testing.T, root, fixture string) {
	t.Helper()
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
	var out, errOut bytes.Buffer
	env := &cliEnv{out: &out, errOut: &errOut}
	if err := env.offerSemanticSearch(context.Background(), bufio.NewReader(strings.NewReader(answer)),
		true, config.Paths{Config: path}, false); err != nil {
		t.Fatal(err)
	}
	return out.String() + errOut.String()
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
