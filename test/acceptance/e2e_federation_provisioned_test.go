//go:build acceptance && e2e_federation

package acceptance

import (
	"os"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

func TestFrozenFederationProvisioned(t *testing.T) {
	requireFrozenFederationPrerequisites(t)
	guardLiveHub(t)
	if err := verifyFrozenDigest(mustAcceptanceRoot(t)); err != nil {
		t.Fatal(err)
	}
	seeded := newFederationLab(t, "main")
	t.Run("uso-de-la-roca-vector", func(t *testing.T) {
		for _, c := range vectorUsoCases {
			t.Run(c.id, func(t *testing.T) { runVectorUsage(t, seeded, c) })
		}
	})
	t.Run("real-usage-vector-query", func(t *testing.T) { caseVectorQueryBudget(t, seeded) })
	t.Run("real-usage-e2e-smoke", TestPublishedReleaseUpdateInitSmoke)
}

func TestFrozenFederationProvisionedJourney(t *testing.T) {
	requireFrozenFederationPrerequisites(t)
	features, err := loadCatalogFeatures("../../features")
	if err != nil {
		t.Fatalf("prepare the features: %v", err)
	}
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("I cannot find the binary: %v", err)
	}
	runGodogTagged(t, features, "@provisioned", func(ctx *godog.ScenarioContext) {
		registerSteps(ctx, binary)
	})
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
