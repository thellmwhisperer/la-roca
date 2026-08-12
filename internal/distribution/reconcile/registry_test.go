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
		name, provider, body, answer, wantOrder, wantAlert, credentialFile string
		binaries                                                           []string
		wantLegacy                                                         bool
	}{
		{
			name: "API key accept with CLI", provider: "xai", answer: "y\n", binaries: []string{"codex"},
			body: legacyAPIConfig(), wantOrder: "codex,ollama", wantAlert: "migrate xai to codex", credentialFile: "xai.key",
		},
		{
			name: "API key decline with CLI", provider: "xai", answer: "n\n", binaries: []string{"codex"},
			body: legacyAPIConfig(), wantOrder: "xai,ollama", wantAlert: "migrate xai to codex", credentialFile: "xai.key", wantLegacy: true,
		},
		{
			name: "OAuth accept with CLI", provider: "codex", answer: "yes\n", binaries: []string{"codex"},
			body: legacyOAuthConfig(), wantOrder: "codex,ollama", wantAlert: "migrate codex to codex", credentialFile: "codex.json",
		},
		{
			name: "OAuth decline with CLI", provider: "codex", answer: "no\n", binaries: []string{"codex"},
			body: legacyOAuthConfig(), wantOrder: "codex,ollama", wantAlert: "migrate codex to codex", credentialFile: "codex.json", wantLegacy: true,
		},
		{
			name: "API key accept without CLI", provider: "xai", answer: "y\n",
			body: legacyAPIConfig(), wantOrder: "ollama", wantAlert: "drop xai", credentialFile: "xai.key",
		},
		{
			name: "OAuth accept without CLI", provider: "codex", answer: "y\n",
			body: legacyOAuthConfig(), wantOrder: "ollama", wantAlert: "drop codex", credentialFile: "codex.json",
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
			credentialPath := filepath.Join(root, "credentials", tc.credentialFile)
			if err := os.MkdirAll(filepath.Dir(credentialPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(credentialPath, []byte("legacy-file-secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			credentialPaths := map[string]string{tc.provider: credentialPath}
			entries := retiredEntries(Open(Context{ConfigPath: path, LookPath: lookPathIn(bin),
				RetiredCredentialPaths: credentialPaths}, Registry()))
			if len(entries) != 1 || !strings.Contains(entries[0].Proposal.Alert, tc.wantAlert) {
				t.Fatalf("entries = %+v, want alert containing %q", entries, tc.wantAlert)
			}
			var out strings.Builder
			result, err := Run(Context{
				Version: "v2", ConfigPath: path, StampPath: filepath.Join(root, "stamp.json"),
				LookPath: lookPathIn(bin), RetiredCredentialPaths: credentialPaths,
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
			if strings.Contains(tc.body, "legacy-secret") && hasLegacy != tc.wantLegacy {
				t.Fatalf("legacy config present=%v, want %v\n%s", hasLegacy, tc.wantLegacy, text)
			}
			_, credentialErr := os.Stat(credentialPath)
			if tc.wantLegacy && credentialErr != nil {
				t.Fatalf("declined legacy credential was removed: %v", credentialErr)
			}
			if !tc.wantLegacy && !os.IsNotExist(credentialErr) {
				t.Fatalf("accepted legacy credential survived: %v", credentialErr)
			}
			if !tc.wantLegacy && strings.Contains(tc.body, "legacy-secret") {
				backup := mustRead(t, path+".roca.bak")
				if strings.Contains(backup, "legacy-secret") {
					t.Fatalf("provider secret survived in recovery backup:\n%s", backup)
				}
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

func TestRetiredCredentialFileRemainsDiscoverableWithoutConfigMarker(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte("[models]\norder = [\"codex\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(root, "credentials", "xai.key")
	if err := os.MkdirAll(filepath.Dir(credential), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credential, []byte("legacy-file-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	context := Context{ConfigPath: path, LookPath: lookPathIn(t.TempDir()),
		RetiredCredentialPaths: map[string]string{"xai": credential}}
	entries := retiredEntries(Open(context, Registry()))
	if len(entries) != 1 || entries[0].RetiredProvider != "xai" {
		t.Fatalf("orphaned credential entries = %+v", entries)
	}
	if err := RemoveRetiredCredential(""); err != nil {
		t.Fatalf("empty legacy credential path: %v", err)
	}
}

func TestRetiredProviderEditsPreserveRawTableIdentity(t *testing.T) {
	root, bin := t.TempDir(), t.TempDir()
	writeExecutable(t, filepath.Join(bin, "codex"))
	path := filepath.Join(root, "config.toml")
	body := "[models]\norder = [\"open_router\", \"ollama\"]\n\n[models.open_router]\napi_key = \"legacy-secret\"\nmodel = \"legacy-model\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	context := Context{Version: "v2", ConfigPath: path,
		StampPath: filepath.Join(root, "stamp.json"), LookPath: lookPathIn(bin)}
	entries := retiredEntries(Open(context, Registry()))
	if len(entries) != 1 || entries[0].RetiredProvider != "open-router" {
		t.Fatalf("entries = %+v", entries)
	}
	result, err := Run(context, entries, Options{
		Interactive: true, In: strings.NewReader("y\n"), Out: &strings.Builder{},
	})
	if err != nil || result.Accepted != 1 {
		t.Fatalf("result = %+v, err %v", result, err)
	}
	updated := mustRead(t, path)
	if strings.Contains(updated, "open_router") || strings.Contains(updated, "legacy-secret") {
		t.Fatalf("raw legacy table survived migration:\n%s", updated)
	}
	backup := mustRead(t, path+".roca.bak")
	if !strings.Contains(backup, "[models.open_router]") || strings.Contains(backup, "legacy-secret") {
		t.Fatalf("backup rewrote raw identity or retained secret:\n%s", backup)
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
	return "# keep\n[models]\norder = [\"xai\", \"ollama\"]\n\n[models.xai]\nbase_url = \"https://example.invalid\"\n\"api_key\" = \"legacy-secret\"\nmodel = \"grok-legacy\"\n"
}

func legacyOAuthConfig() string {
	return "# keep\n[models]\norder = [\"codex\", \"ollama\"]\n\n[models.codex]\nmodel = \"gpt-preserved\"\n"
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
