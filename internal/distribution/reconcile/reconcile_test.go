package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestRunnerOffersEachOpenProposalOncePerVersion(t *testing.T) {
	dir := t.TempDir()
	registry := []Entry{{
		ID:        "new-capability",
		Detection: Detection{Capability: "available"},
		Proposal:  Proposal{Alert: "new capability is available"},
	}}
	context := Context{
		Version: "v2.0.0", ConfigPath: filepath.Join(dir, "config.toml"),
		StampPath:    filepath.Join(dir, "reconciliation.json"),
		Capabilities: map[string]bool{"available": true},
	}

	var first strings.Builder
	result, err := Run(context, registry, Options{Out: &first})
	if err != nil {
		t.Fatal(err)
	}
	if result.Offered != 1 || !strings.Contains(first.String(), "new capability") {
		t.Fatalf("first run = %+v, output %q", result, first.String())
	}

	var repeated strings.Builder
	result, err = Run(context, registry, Options{Out: &repeated})
	if err != nil {
		t.Fatal(err)
	}
	if result.Offered != 0 || repeated.Len() != 0 {
		t.Fatalf("same-version run = %+v, output %q", result, repeated.String())
	}

	context.Version = "v2.1.0"
	result, err = Run(context, registry, Options{Out: &repeated})
	if err != nil || result.Offered != 1 {
		t.Fatalf("new-version run = %+v, err %v", result, err)
	}
}

func TestTTYAcceptanceWritesSurgicallyWithBackupAndNamesTheResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	before := "# operator note\n[models]\ntimeout_ms = 9000\norder = [\"codex\", \"ollama\"]\n"
	if err := os.WriteFile(path, []byte(before), 0o640); err != nil {
		t.Fatal(err)
	}
	registry := []Entry{{
		ID:        "enable-claude",
		Detection: Detection{Capability: "claude"},
		Proposal: Proposal{
			Alert: "Claude CLI can answer locally", Prompt: "Enable Claude?",
			Changes: []config.Change{{
				Kind: config.PrependUnique, Table: "models", Key: "order",
				Value: "claude", Default: []string{"codex", "ollama"},
			}},
		},
	}}
	var out strings.Builder
	result, err := Run(Context{
		Version: "v2", ConfigPath: path, StampPath: filepath.Join(dir, "stamp.json"),
		Capabilities: map[string]bool{"claude": true},
	}, registry, Options{Interactive: true, In: strings.NewReader("y\n"), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 1 || len(result.Changes) != 1 {
		t.Fatalf("result = %+v", result)
	}
	change := result.Changes[0]
	if change.Backup == "" || !strings.Contains(out.String(), path) ||
		!strings.Contains(out.String(), change.Backup) {
		t.Fatalf("write result was not named: %+v\n%s", change, out.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "# operator note") ||
		!strings.Contains(string(after), `order = ["claude", "codex", "ollama"]`) {
		t.Fatalf("configuration was not surgically edited:\n%s", after)
	}
	backup, err := os.ReadFile(change.Backup)
	if err != nil || string(backup) != before {
		t.Fatalf("backup = %q, err %v", backup, err)
	}
}

func TestNonTTYAlertsWithoutPromptingOrChangingConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[models]\norder = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := []Entry{{
		ID: "capability", Detection: Detection{Capability: "ready"},
		Proposal: Proposal{Alert: "capability needs a look", Prompt: "Enable it?",
			Changes: []config.Change{{Kind: config.SetValue, Table: "models.demo", Key: "model", Value: "demo"}}},
	}}
	var out strings.Builder
	result, err := Run(Context{
		Version: "v2", ConfigPath: path, StampPath: filepath.Join(dir, "stamp.json"),
		Capabilities: map[string]bool{"ready": true},
	}, registry, Options{Out: &out, In: strings.NewReader("y\n")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 0 || strings.Count(strings.TrimSpace(out.String()), "\n") != 0 ||
		!strings.Contains(out.String(), "capability needs a look") {
		t.Fatalf("non-TTY result = %+v, output %q", result, out.String())
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "models.demo") {
		t.Fatalf("non-TTY run changed config:\n%s", raw)
	}

	var doctor strings.Builder
	result, err = Run(Context{
		Version: "v2", ConfigPath: path, StampPath: filepath.Join(dir, "stamp.json"),
		Capabilities: map[string]bool{"ready": true},
	}, registry, Options{Interactive: true, ListAll: true,
		Out: &doctor, In: strings.NewReader("y\n")})
	if err != nil || result.Accepted != 1 || !strings.Contains(doctor.String(), "Enable it?") {
		t.Fatalf("doctor retry = %+v, err %v, output %q", result, err, doctor.String())
	}
}
