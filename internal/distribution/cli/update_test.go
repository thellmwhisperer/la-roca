package cli

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/release"
)

func TestUpdateRefusesAnInsecureOrMalformedMirror(t *testing.T) {
	env := &cliEnv{}
	t.Setenv("HOME", t.TempDir())
	for _, api := range []string{"http://mirror.example", "https://mirror.example?repo=other", "https://mirror.example/api/../other"} {
		if _, err := env.releaseSource("owner/repo", api); err == nil {
			t.Errorf("API %q was accepted", api)
		}
	}
	for _, repo := range []string{"not-a-repo", "owner/%2Frepo"} {
		if _, err := env.releaseSource(repo, "https://mirror.example/api/v3"); err == nil || !strings.Contains(err.Error(), "owner/name") {
			t.Fatalf("malformed repository %q refusal = %v", repo, err)
		}
	}
}

func TestUpdateAcceptsATrustedHTTPSMirrorShape(t *testing.T) {
	env := &cliEnv{}
	t.Setenv("HOME", t.TempDir())
	source, err := env.releaseSource("owner/repo", "https://mirror.example/api/v3")
	if err != nil {
		t.Fatal(err)
	}
	if source.API != "https://mirror.example/api/v3" {
		t.Fatalf("API = %q", source.API)
	}
}

// An operator who installed this product and types `roca update` has already
// said which repository they trust: the one the binary they are running came
// from. Making them repeat it in a flag is redundant. Precedence is the flag,
// then the
// environment, then the configuration, and only then the channel this product
// publishes from.
func TestUpdateFallsBackToTheRepositoryThisProductPublishesFrom(t *testing.T) {
	env := &cliEnv{}
	t.Setenv("HOME", t.TempDir())
	t.Setenv(release.EnvRepo, "")

	source, err := env.releaseSource("", "")
	if err != nil {
		t.Fatal(err)
	}
	if source.Repo != release.DefaultRepo {
		t.Errorf("repo = %q, want the default %q", source.Repo, release.DefaultRepo)
	}
}

// What an operator names still wins over it, or pinning an update to a fork or
// to a mirror stops working the day a default appears.
func TestAnOperatorsOwnRepositoryStillWins(t *testing.T) {
	env := &cliEnv{}
	t.Setenv("HOME", t.TempDir())
	t.Setenv(release.EnvRepo, "from/the-environment")

	flagged, err := env.releaseSource("from/the-flag", "")
	if err != nil {
		t.Fatal(err)
	}
	if flagged.Repo != "from/the-flag" {
		t.Errorf("repo = %q, want the flag's", flagged.Repo)
	}

	fromEnv, err := env.releaseSource("", "")
	if err != nil {
		t.Fatal(err)
	}
	if fromEnv.Repo != "from/the-environment" {
		t.Errorf("repo = %q, want the environment's", fromEnv.Repo)
	}
}
