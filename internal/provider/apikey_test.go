package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAPIKeyTrimsAndRoundTrips(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := SaveAPIKey(dir, NameXAI, " sk-xai-secret "); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadAPIKey(dir, NameXAI)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != "sk-xai-secret" {
		t.Fatalf("loaded %q, want the trimmed key", got)
	}
}

func TestDeleteAPIKeyForgetsTheCredential(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := SaveAPIKey(dir, NameDeepSeek, "sk-ds"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := DeleteAPIKey(dir, NameDeepSeek); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := LoadAPIKey(dir, NameDeepSeek)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != "" {
		t.Fatalf("the key is still there: %q", got)
	}
	// Forgetting what was already forgotten is not a failure.
	if err := DeleteAPIKey(dir, NameDeepSeek); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestSaveAPIKeyRefusesAnEmptyKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := SaveAPIKey(dir, NameZAI, "   "); err == nil {
		t.Fatal("an empty key has to be refused")
	}
	if _, err := os.Stat(APIKeyPath(dir, NameZAI)); !os.IsNotExist(err) {
		t.Fatalf("an empty key must not leave a file: %v", err)
	}
}

func TestAStoredKeyFeedsTheCascadeAndBeatsTheConfig(t *testing.T) {
	base := settings(t, `
[models]
order = ["xai"]

[models.xai]
api_key = "from-config"
`)
	if err := SaveAPIKey(base.Credentials, NameXAI, "from-login"); err != nil {
		t.Fatalf("save: %v", err)
	}

	cascade, err := BuildCascade(base)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	openai, ok := cascade.Providers[0].(*OpenAICompatible)
	if !ok {
		t.Fatalf("provider type %T", cascade.Providers[0])
	}
	if !openai.HasCredential() {
		t.Fatal("the stored key did not reach the adapter")
	}

	// Store alone is enough: no config api_key required.
	onlyStore := settings(t, "[models]\norder = [\"xai\"]\n")
	onlyStore.Credentials = base.Credentials
	cascade2, err := BuildCascade(onlyStore)
	if err != nil {
		t.Fatalf("build store-only: %v", err)
	}
	if !cascade2.Providers[0].(*OpenAICompatible).HasCredential() {
		t.Fatal("the stored key alone does not feed the cascade")
	}

	// After logout the config api_key still works.
	if err := DeleteAPIKey(base.Credentials, NameXAI); err != nil {
		t.Fatalf("delete: %v", err)
	}
	cascade3, err := BuildCascade(base)
	if err != nil {
		t.Fatalf("build after delete: %v", err)
	}
	if !cascade3.Providers[0].(*OpenAICompatible).HasCredential() {
		t.Fatal("config api_key has to keep working after logout")
	}
}
