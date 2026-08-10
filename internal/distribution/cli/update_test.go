package cli

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/release"
)

// An operator who installed this product and types `roca update` has already
// said which repository they trust: the one the binary they are running came
// from. Making them repeat it in a flag is asking a question whose answer is
// decided, and the D-1 order still stands over it: the flag, then the
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
