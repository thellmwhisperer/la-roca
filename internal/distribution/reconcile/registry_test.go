package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestLaunchRegistryDetections(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("fixture"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	credentials := writeCodexCredential(t, root)

	cases := []struct {
		name, configText, proposal string
		want                       bool
	}{
		{"claude gap", "[models]\norder = [\"ollama\"]\n", ProposalClaudeCLI, true},
		{"unusable claude HTTP", "[models]\norder = [\"claude\"]\n[models.claude]\nbase_url = \"https://example.invalid\"\nmodel = \"remote\"\n", ProposalClaudeCLI, true},
		{"usable claude HTTP", "[models]\norder = [\"claude\"]\n[models.claude]\nbase_url = \"https://example.invalid\"\nmodel = \"remote\"\napi_key = \"synthetic-credential\"\n", ProposalClaudeCLI, false},
		{"codex migration", "[models]\norder = [\"codex\"]\n", ProposalCodexCLI, true},
		{"anthropic export gap", "[defaults]\n", ProposalAnthropicExport, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name+".toml")
			if err := os.WriteFile(path, []byte(tc.configText), 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := config.LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			open := Open(Context{
				ConfigPath: path, CredentialsPath: credentials, File: file,
				LookPath: lookPathIn(bin), Capabilities: map[string]bool{CapabilityAnthropicExport: true},
			}, Registry())
			if got := containsProposal(open, tc.proposal); got != tc.want {
				t.Fatalf("open proposals = %v, contains %s = %v, want %v",
					proposalIDs(open), tc.proposal, got, tc.want)
			}
		})
	}
}

func TestCodexMigrationReplacesHTTPSettingsWithTheDeclaredCommand(t *testing.T) {
	root, bin := t.TempDir(), t.TempDir()
	writeExecutable(t, filepath.Join(bin, "codex"))
	credentials := writeCodexCredential(t, root)
	path := filepath.Join(root, "config.toml")
	before := "# keep\n[models]\norder = [\"codex\"]\n\n[models.codex]\nbase_url = \"https://example.invalid\"\nmodel = \"gpt-preserved\"\n\n[[audit_entries]]\nname = \"keep-array\"\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	result, err := Run(Context{
		Version: "v2", ConfigPath: path, StampPath: filepath.Join(root, "stamp.json"),
		CredentialsPath: credentials, LookPath: lookPathIn(bin),
		Capabilities: map[string]bool{CapabilityAnthropicExport: true},
	}, only(ProposalCodexCLI), Options{Interactive: true, In: strings.NewReader("y\n"), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 1 || result.Changes[0].Backup == "" {
		t.Fatalf("result = %+v", result)
	}
	raw, _ := os.ReadFile(path)
	text := string(raw)
	for _, want := range []string{"# keep", `command = ["codex", "exec"`,
		`model = "gpt-preserved"`, "[[audit_entries]]", `name = "keep-array"`} {
		if !strings.Contains(text, want) {
			t.Errorf("migration omitted %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "base_url") {
		t.Errorf("HTTP transport survived migration:\n%s", text)
	}
}

func TestClaudeProposalReplacesUnusableHTTPWithSonnetPreset(t *testing.T) {
	root, bin := t.TempDir(), t.TempDir()
	writeExecutable(t, filepath.Join(bin, "claude"))
	path := filepath.Join(root, "config.toml")
	before := "# keep\n[models]\norder = [\"claude\", \"ollama\"]\n\n[models.claude]\nbase_url = \"https://example.invalid\"\nmodel = \"remote\"\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Run(Context{
		Version: "v2", ConfigPath: path, StampPath: filepath.Join(root, "stamp.json"),
		CredentialsPath: filepath.Join(root, "credentials"), LookPath: lookPathIn(bin),
	}, only(ProposalClaudeCLI), Options{
		Interactive: true, In: strings.NewReader("y\n"), Out: &strings.Builder{},
	})
	if err != nil || result.Accepted != 1 {
		t.Fatalf("result = %+v, err %v", result, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "# keep") || strings.Contains(string(raw), "base_url") ||
		strings.Contains(string(raw), `model = "remote"`) {
		t.Fatalf("Claude HTTP settings survived preset enablement:\n%s", raw)
	}
	file, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cascade, err := provider.BuildCascade(provider.Settings{
		File: file, RunnerDir: filepath.Join(root, "runner"),
	})
	if err != nil || len(cascade.Providers) == 0 || cascade.Providers[0].ModelID() != "sonnet" {
		t.Fatalf("Claude preset = %+v, err %v", cascade, err)
	}
}

func TestAnthropicExportProposalWritesTheFolderTheOperatorTypes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte("# keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Run(Context{
		Version: "v2", ConfigPath: path, StampPath: filepath.Join(root, "stamp.json"),
		Capabilities: map[string]bool{CapabilityAnthropicExport: true},
	}, only(ProposalAnthropicExport), Options{
		Interactive: true, In: strings.NewReader("yes\n~/exports/claude\n"), Out: &strings.Builder{},
	})
	if err != nil || result.Accepted != 1 {
		t.Fatalf("result = %+v, err %v", result, err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `anthropic_export_paths = ["~/exports/claude"]`) ||
		!strings.Contains(string(raw), "# keep") {
		t.Fatalf("export path was not surgically added:\n%s", raw)
	}
}

func only(id string) []Entry {
	for _, entry := range Registry() {
		if entry.ID == id {
			return []Entry{entry}
		}
	}
	return nil
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeCodexCredential(t *testing.T, root string) string {
	t.Helper()
	credentials := filepath.Join(root, "credentials")
	if err := os.MkdirAll(credentials, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credentials, "codex.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return credentials
}

func TestLaunchRegistryClosesConfiguredCases(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	configured := `[defaults]
anthropic_export_paths = ["/exports"]

[models]
order = ["claude", "codex"]

[models.codex]
command = ["codex", "exec", "-"]
model = "gpt-test"
`
	if err := os.WriteFile(path, []byte(configured), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if open := Open(Context{
		ConfigPath: path, CredentialsPath: filepath.Join(root, "credentials"), File: file,
		LookPath: lookPathIn(root), Capabilities: map[string]bool{CapabilityAnthropicExport: true},
	}, Registry()); len(open) != 0 {
		t.Fatalf("configured environment still has proposals: %v", proposalIDs(open))
	}
}

func lookPathIn(directory string) func(string) (string, error) {
	return func(name string) (string, error) {
		path := filepath.Join(directory, name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return "", os.ErrNotExist
		}
		return path, nil
	}
}

func containsProposal(entries []Entry, id string) bool {
	for _, entry := range entries {
		if entry.ID == id {
			return true
		}
	}
	return false
}

func proposalIDs(entries []Entry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}
