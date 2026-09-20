//go:build acceptance && e2e_federation

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

func TestE2EFederationJourney(t *testing.T) {
	requireFrozenFederationPrerequisites(t)
	requireInstalledCandidate(t)
	guardLiveHub(t)
	root := mustAcceptanceRoot(t)
	if err := verifyFrozenDigest(root); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "features", "distribution", "e2e-federation.feature"))
	if err != nil {
		t.Fatalf("prepare the federation feature: %v", err)
	}
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("I cannot find the binary: %v", err)
	}
	runGodogTagged(t, []godog.Feature{{
		Name:     "distribution/e2e-federation.feature",
		Contents: raw,
	}}, "@e2e-federation", func(ctx *godog.ScenarioContext) {
		registerSteps(ctx, binary)
	})
}

func requireInstalledCandidate(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("ROCA_BIN")) == "" {
		t.Fatal("ROCA_BIN is required; select an installed candidate executable explicitly")
	}
	binary, err := rocaBinary()
	if err != nil {
		t.Fatal(err)
	}
	built := filepath.Join(mustAcceptanceRoot(t), "bin", "roca")
	if filepath.Clean(binary) == filepath.Clean(built) {
		t.Fatal("e2e-federation must run the installed candidate binary, not the just-built worktree binary")
	}
}

func requireFrozenFederationPrerequisites(t *testing.T) {
	t.Helper()
	model := strings.TrimSpace(os.Getenv("ROCA_E2E_VECTOR_MODEL"))
	if model == "" {
		t.Fatal("ROCA_E2E_VECTOR_MODEL is required; the ready-index path cannot pass by skipping")
	}
	if info, err := os.Stat(model); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("ROCA_E2E_VECTOR_MODEL %s is not a regular file: %v", model, err)
	}
	published := strings.TrimSpace(os.Getenv("ROCA_PUBLISHED_BIN"))
	if published == "" {
		t.Fatal("ROCA_PUBLISHED_BIN is required; the published upgrade path cannot pass by skipping")
	}
	info, err := os.Stat(published)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("ROCA_PUBLISHED_BIN %s is not executable: %v", published, err)
	}
}
