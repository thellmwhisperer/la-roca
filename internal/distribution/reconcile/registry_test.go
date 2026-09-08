package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
