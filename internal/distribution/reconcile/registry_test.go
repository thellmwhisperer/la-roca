package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestRetiredProviderFirstRunAcceptAndDeclinePaths(t *testing.T) {
	cases := []struct {
		name, provider, body, answer, wantOrder, wantAlert string
		binaries                                           []string
		wantLegacy                                         bool
	}{
		{
			name: "API key accept with CLI", provider: "xai", answer: "y\n", binaries: []string{"codex"},
			body: legacyAPIConfig(), wantOrder: "codex,ollama", wantAlert: "migrate xai to codex",
		},
		{
			name: "API key decline with CLI", provider: "xai", answer: "n\n", binaries: []string{"codex"},
			body: legacyAPIConfig(), wantOrder: "xai,ollama", wantAlert: "migrate xai to codex", wantLegacy: true,
		},
		{
			name: "OAuth accept with CLI", provider: "codex", answer: "yes\n", binaries: []string{"codex"},
			body: legacyOAuthConfig(), wantOrder: "codex,ollama", wantAlert: "migrate codex to codex",
		},
		{
			name: "OAuth decline with CLI", provider: "codex", answer: "no\n", binaries: []string{"codex"},
			body: legacyOAuthConfig(), wantOrder: "codex,ollama", wantAlert: "migrate codex to codex", wantLegacy: true,
		},
		{
			name: "API key accept without CLI", provider: "xai", answer: "y\n",
			body: legacyAPIConfig(), wantOrder: "ollama", wantAlert: "drop xai",
		},
		{
			name: "OAuth accept without CLI", provider: "codex", answer: "y\n",
			body: legacyOAuthConfig(), wantOrder: "ollama", wantAlert: "drop codex",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, bin := t.TempDir(), t.TempDir()
			for _, name := range tc.binaries {
				writeExecutable(t, filepath.Join(bin, name))
			}
			path := filepath.Join(root, "config.toml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			entries := retiredEntries(Open(Context{ConfigPath: path, LookPath: lookPathIn(bin)}, Registry()))
			if len(entries) != 1 || !strings.Contains(entries[0].Proposal.Alert, tc.wantAlert) {
				t.Fatalf("entries = %+v, want alert containing %q", entries, tc.wantAlert)
			}
			var out strings.Builder
			result, err := Run(Context{
				Version: "v2", ConfigPath: path, StampPath: filepath.Join(root, "stamp.json"),
				LookPath: lookPathIn(bin),
			}, entries, Options{Interactive: true, In: strings.NewReader(tc.answer), Out: &out})
			if err != nil {
				t.Fatal(err)
			}
			wantAccepted := !tc.wantLegacy
			if (result.Accepted == 1) != wantAccepted {
				t.Fatalf("result = %+v", result)
			}
			text := mustRead(t, path)
			file, err := config.LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(file.Models.Order, ","); got != tc.wantOrder {
				t.Fatalf("order = %q, want %q\n%s", got, tc.wantOrder, text)
			}
			hasLegacy := strings.Contains(text, "legacy-secret") || strings.Contains(text, "base_url")
			if hasLegacy != tc.wantLegacy {
				t.Fatalf("legacy config present=%v, want %v\n%s", hasLegacy, tc.wantLegacy, text)
			}
			if !strings.Contains(text, "# keep") {
				t.Fatalf("operator comment was lost:\n%s", text)
			}
		})
	}
}

func TestRetiredProviderNonTTYAlertIsOneLine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte(legacyAPIConfig()), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	result, err := Run(Context{
		Version: "v2", ConfigPath: path, StampPath: filepath.Join(root, "stamp.json"),
		LookPath: lookPathIn(t.TempDir()),
	}, Registry(), Options{Out: &out})
	if err != nil || result.Offered != 1 || result.Accepted != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if lines := strings.Count(strings.TrimSpace(out.String()), "\n") + 1; lines != 1 ||
		!strings.HasPrefix(out.String(), "capability: ") {
		t.Fatalf("non-TTY alert = %q", out.String())
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
	text := mustRead(t, path)
	if !strings.Contains(text, `anthropic_export_paths = ["~/exports/claude"]`) || !strings.Contains(text, "# keep") {
		t.Fatalf("export path was not surgically added:\n%s", text)
	}
}

func legacyAPIConfig() string {
	return "# keep\n[models]\norder = [\"xai\", \"ollama\"]\n\n[models.xai]\nbase_url = \"https://example.invalid\"\napi_key = \"legacy-secret\"\nmodel = \"grok-legacy\"\n"
}

func legacyOAuthConfig() string {
	return "# keep\n[models]\norder = [\"codex\", \"ollama\"]\n\n[models.codex]\nbase_url = \"https://chatgpt.com/backend-api/codex\"\napi_key = \"legacy-secret\"\nmodel = \"gpt-preserved\"\n"
}

func retiredEntries(entries []Entry) []Entry {
	var retired []Entry
	for _, entry := range entries {
		if strings.HasPrefix(entry.ID, ProposalRetiredProvider+"-") {
			retired = append(retired, entry)
		}
	}
	return retired
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

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
