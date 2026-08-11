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
	if _, err := env.releaseSource("owner/repo", "https://mirror.example"); err != nil {
		t.Fatalf("root mirror URL was refused: %v", err)
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

// `roca update` replaces the running executable. A build that is not a published
// release tag is somebody's working copy: `git describe` gives `v0.1.0-5-gabc`,
// `v0.1.0-dirty` or a bare commit, and none of those equals the tag it would be
// compared against, so the updater treated every one of them as out of date and
// overwrote the operator's own build with a release.
//
// A clean release tag keeps updating exactly as before.
func TestUpdateRefusesToReplaceABuildThatIsNotAReleaseTag(t *testing.T) {
	for _, want := range []struct {
		version  string
		replaces bool
	}{
		{version: "v0.1.0", replaces: true},
		{version: "0.1.0", replaces: true},
		{version: "v0.1.0-5-gabc1234", replaces: false},
		{version: "v0.1.0-dirty", replaces: false},
		{version: "abc1234", replaces: false},
		{version: "dev", replaces: false},
		{version: "", replaces: false},
	} {
		if got := isReleaseBuild(want.version); got != want.replaces {
			t.Errorf("isReleaseBuild(%q) = %v, want %v", want.version, got, want.replaces)
		}
	}
}

// The refusal names what the operator can do instead, and it is not an update
// that silently did nothing.
func TestTheRefusalToSelfReplaceSaysWhatToDoInstead(t *testing.T) {
	var out strings.Builder
	env := &cliEnv{out: &out, errOut: &out, build: Build{Version: "v0.1.0-dirty"}}

	err := env.refuseSelfReplacement("v0.2.0")

	if err == nil {
		t.Fatal("a development build was allowed to replace itself")
	}
	for _, want := range []string{"v0.1.0-dirty", "v0.2.0", "install.sh"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

func TestCapabilityCountRequiresThePendingField(t *testing.T) {
	if _, err := decodeCapabilityCount([]byte(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "missing pending") {
		t.Fatalf("missing pending field error = %v", err)
	}
	if count, err := decodeCapabilityCount([]byte(`{"pending":2}`)); err != nil || count != 2 {
		t.Fatalf("count = %d, err %v", count, err)
	}
}
