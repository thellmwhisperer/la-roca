package provider_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/oauth"
)

func TestCredentialWritesConvergeOnOperatorOnlyPermissions(t *testing.T) {
	cases := []struct {
		name        string
		filename    string
		initialMode os.FileMode
		write       func(string) error
	}{
		{"new API key", provider.APIKeyFile(provider.NameXAI), 0, func(path string) error {
			return provider.SaveAPIKey(filepath.Dir(path), provider.NameXAI, "secret")
		}},
		{"existing API key", provider.APIKeyFile(provider.NameXAI), 0o644, func(path string) error {
			return provider.SaveAPIKey(filepath.Dir(path), provider.NameXAI, "secret")
		}},
		{"new OAuth token", "codex.json", 0, func(path string) error {
			return (oauth.Store{Path: path}).Save(oauth.Token{AccessToken: "access"})
		}},
		{"existing OAuth token", "codex.json", 0o644, func(path string) error {
			return (oauth.Store{Path: path}).Save(oauth.Token{AccessToken: "access"})
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dir := looseCredentialDirectory(t)
			path := filepath.Join(dir, test.filename)
			if test.initialMode != 0 {
				if err := os.WriteFile(path, []byte("old"), test.initialMode); err != nil {
					t.Fatal(err)
				}
			}
			if err := test.write(path); err != nil {
				t.Fatal(err)
			}
			assertPermission(t, dir, 0o700)
			assertPermission(t, path, 0o600)
		})
	}
}

func looseCredentialDirectory(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertPermission(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
