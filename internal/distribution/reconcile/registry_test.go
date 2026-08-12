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
		wantKept                                                           string
		wantLegacy                                                         bool
		preexistingBackups                                                 bool
	}{
		{
			name: "API key accept with CLI", provider: "xai", answer: "y\n", binaries: []string{"codex"},
			body: legacyAPIConfig(), wantOrder: "codex,ollama", wantAlert: "migrate xai to codex", credentialFile: "xai.key",
			preexistingBackups: true,
		},
		{
			name: "API key decline with CLI", provider: "xai", answer: "n\n", binaries: []string{"codex"},
			body: legacyAPIConfig(), wantOrder: "xai,ollama", wantAlert: "migrate xai to codex", credentialFile: "xai.key", wantLegacy: true,
		},
		{
			name: "OAuth accept with CLI", provider: "codex", answer: "yes\n", binaries: []string{"codex"},
			body: legacyOAuthConfig(), wantOrder: "codex,ollama", credentialFile: "codex.json",
			wantAlert: "remove the retired codex authentication settings", wantKept: `model = "gpt-preserved"`,
		},
		{
			name: "OAuth decline with CLI", provider: "codex", answer: "no\n", binaries: []string{"codex"},
			body: legacyOAuthConfig(), wantOrder: "codex,ollama", credentialFile: "codex.json", wantLegacy: true,
			wantAlert: "remove the retired codex authentication settings",
		},
		{
			name: "API key accept without CLI", provider: "xai", answer: "y\n",
			body: legacyAPIConfig(), wantOrder: "ollama", wantAlert: "drop xai", credentialFile: "xai.key",
		},
		{
			name: "OAuth accept without CLI", provider: "codex", answer: "y\n",
			body: legacyOAuthConfig(), wantOrder: "ollama", wantAlert: "drop codex", credentialFile: "codex.json",
		},
		{
			// The operator's own command is the transport, so migration never
			// moves this provider elsewhere and never deletes its table.
			name: "explicit command accept with another CLI", provider: "codex", answer: "y\n",
			binaries: []string{"claude"}, body: legacyCommandConfig(), wantOrder: "codex,ollama",
			wantAlert: "remove the retired codex authentication settings", credentialFile: "codex.json",
			wantKept: `command = ["synthetic-codex", "exec"]`,
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
			var recoveryBackupPaths []string
			if tc.preexistingBackups {
				for _, suffix := range []string{".roca.bak", ".roca.bak.2"} {
					backup := path + suffix
					if err := os.WriteFile(backup, []byte(tc.body), 0o600); err != nil {
						t.Fatal(err)
					}
					recoveryBackupPaths = append(recoveryBackupPaths, backup)
				}
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
				RecoveryBackupPaths: recoveryBackupPaths,
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
			if tc.wantLegacy && strings.Contains(tc.body, "gpt-preserved") &&
				!strings.Contains(text, `model = "gpt-preserved"`) {
				t.Fatalf("declined OAuth configuration lost its model:\n%s", text)
			}
			_, credentialErr := os.Stat(credentialPath)
			if tc.wantLegacy && credentialErr != nil {
				t.Fatalf("declined legacy credential was removed: %v", credentialErr)
			}
			if !tc.wantLegacy && !os.IsNotExist(credentialErr) {
				t.Fatalf("accepted legacy credential survived: %v", credentialErr)
			}
			if !tc.wantLegacy && strings.Contains(tc.body, "legacy-secret") {
				backups := append([]string(nil), recoveryBackupPaths...)
				for _, change := range result.Changes {
					if change.Backup != "" {
						backups = append(backups, change.Backup)
					}
				}
				for _, backupPath := range backups {
					backup := mustRead(t, backupPath)
					if strings.Contains(backup, "legacy-secret") {
						t.Fatalf("provider secret survived in recovery backup %s:\n%s", backupPath, backup)
					}
				}
			}
			if tc.wantKept != "" && !strings.Contains(text, tc.wantKept) {
				t.Fatalf("retirement lost %q:\n%s", tc.wantKept, text)
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

func TestRetiredCredentialFilesRemainDiscoverableWithoutRetiringCommands(t *testing.T) {
	cases := []struct {
		name, provider, credentialFile, body string
		wantCommandAlert                     bool
	}{
		{name: "orphaned artifact", provider: "xai", credentialFile: "xai.key",
			body: "[models]\norder = [\"codex\"]\n"},
		{name: "explicit command", provider: "codex", credentialFile: "codex.json", wantCommandAlert: true,
			body: "[models]\norder = [\"codex\"]\n\n[models.codex]\ncommand = [\"custom-codex\", \"exec\"]\nmodel = \"gpt-current\"\n"},
		{name: "shipped CLI preset", provider: "codex", credentialFile: "codex.json",
			body: "[models]\norder = [\"codex\", \"ollama\"]\n\n[models.codex]\nmodel = \"gpt-preserved\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config.toml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			credential := filepath.Join(root, "credentials", tc.credentialFile)
			if err := os.MkdirAll(filepath.Dir(credential), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(credential, []byte("legacy-file-secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			context := Context{Version: "v2", ConfigPath: path, StampPath: filepath.Join(root, "stamp.json"),
				LookPath: lookPathIn(t.TempDir()), RetiredCredentialPaths: map[string]string{tc.provider: credential}}
			entries := retiredEntries(Open(context, Registry()))
			if len(entries) != 1 || entries[0].RetiredProvider != tc.provider || len(entries[0].Proposal.Changes) != 0 {
				t.Fatalf("credential cleanup entries = %+v", entries)
			}
			if tc.wantCommandAlert && !strings.Contains(entries[0].Proposal.Alert, "command transport remains unchanged") {
				t.Fatalf("command-preserving alert = %q", entries[0].Proposal.Alert)
			}
			result, err := Run(context, entries, Options{
				Interactive: true, In: strings.NewReader("y\n"), Out: &strings.Builder{},
			})
			if err != nil || result.Accepted != 1 || len(result.Changes) != 0 {
				t.Fatalf("result = %+v, err %v", result, err)
			}
			if updated := mustRead(t, path); updated != tc.body {
				t.Fatalf("credential cleanup changed command configuration:\n%s", updated)
			}
			if _, err := os.Stat(credential); !os.IsNotExist(err) {
				t.Fatalf("retired credential survived cleanup: %v", err)
			}
		})
	}
	if err := RemoveRetiredCredential(""); err != nil {
		t.Fatalf("empty legacy credential path: %v", err)
	}
	root := t.TempDir()
	direct := filepath.Join(root, "legacy.key")
	if err := os.WriteFile(direct, []byte("legacy-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRetiredCredential(direct); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("credential cleanup removed a non-credential parent: %v", err)
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
	return "# keep\n[models]\norder = [\"codex\", \"ollama\"]\n\n[models.codex]\n" +
		"base_url = \"https://synthetic.invalid/backend-api/codex\"\nmodel = \"gpt-preserved\"\n"
}

func legacyCommandConfig() string {
	return "# keep\n[models]\norder = [\"codex\", \"ollama\"]\n\n[models.codex]\n" +
		"command = [\"synthetic-codex\", \"exec\"]\n\"api_key\" = \"legacy-secret\"\nmodel = \"gpt-preserved\"\n"
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
