package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/release"
	"github.com/thellmwhisperer/la-roca/internal/distribution/skill"
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

func TestCapabilityCountUsesTheSelectedDatabase(t *testing.T) {
	command := capabilityCountCommand(context.Background(), "/installed/roca", "/custom/roca.db")
	want := "/installed/roca _capabilities --json --db-path /custom/roca.db"
	if got := strings.Join(command.Args, " "); got != want {
		t.Fatalf("capability count command = %q, want %q", got, want)
	}
}

func TestArtifactRefreshHonoursTheDefaultOffGateAndSystemDivergence(t *testing.T) {
	tests := []struct {
		name, config, current string
		force                 bool
		wantChanged, diverged bool
	}{
		{name: "flag off", current: "shipped-v1\n"},
		{name: "flag on", config: "[features]\nartifact_refresh = true\n", current: "shipped-v1\n", wantChanged: true},
		{name: "edited system", config: "[features]\nartifact_refresh = true\n", current: "operator edit\n", diverged: true},
		{name: "forced edit", config: "[features]\nartifact_refresh = true\n", current: "operator edit\n", force: true, wantChanged: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := skillTestHome(t)
			path := filepath.Join(home, ".codex", "skills", "roca", "SKILL.md")
			writeFile(t, path, artifact.Zoned(test.current, "operator bytes\n"))
			registryPath := filepath.Join(home, ".roca", "artifacts.json")
			if err := artifact.SaveRegistry(registryPath, artifact.Registry{Entries: []artifact.Entry{{
				Kind: "skill", Runtime: "codex", Path: path, InstalledVersion: "v1.0.0",
				SystemSHA256: artifact.Checksum("shipped-v1\n"),
			}}}); err != nil {
				t.Fatal(err)
			}
			if test.config != "" {
				writeFile(t, filepath.Join(home, ".roca", "config.toml"), test.config)
			}
			env := &cliEnv{build: Build{Version: "v2.0.0"}}
			report, err := env.refreshManagedArtifacts(filepath.Join(home, "bin", "roca"), test.force)
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			zones, err := artifact.Parse(string(body))
			if err != nil {
				t.Fatal(err)
			}
			if got := zones.System == skill.Content(); got != test.wantChanged {
				t.Fatalf("system refreshed = %v, want %v; system=%q", got, test.wantChanged, zones.System)
			}
			if zones.User != "operator bytes\n" {
				t.Fatalf("user zone changed: %q", zones.User)
			}
			if got := len(report.Diverged) == 1; got != test.diverged {
				t.Fatalf("report = %+v", report)
			}
			registry, err := artifact.LoadRegistry(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			entry, _ := registry.Find("skill", "codex", path)
			if entry.AvailableVersion != "v2.0.0" {
				t.Fatalf("outdated version was not recorded: %+v", entry)
			}
			if !test.wantChanged && entry.InstalledVersion != "v1.0.0" {
				t.Fatalf("unrefreshed install version changed: %+v", entry)
			}
		})
	}
}

// The update report is what the operator is told, so it has to distinguish the
// three states a refresh can refuse in: an edited SYSTEM zone, a file that is
// no longer there, and one nothing can read. The unreadable one in particular
// must not hide whether the other registered artifacts are current.
func TestArtifactRefreshReportsDeletedAndUnreadableApartFromEdits(t *testing.T) {
	home := skillTestHome(t)
	deleted := filepath.Join(home, ".codex", "skills", "roca", "SKILL.md")
	unreadable := filepath.Join(home, ".hermes", "skills", "roca", "SKILL.md")
	current := filepath.Join(home, ".claude", "skills", "roca", "SKILL.md")
	writeFile(t, unreadable, artifact.Zoned(skill.Content(), "")+"appended after the last marker\n")
	writeFile(t, current, artifact.Zoned(skill.Content(), ""))
	registryPath := filepath.Join(home, ".roca", "artifacts.json")
	entryOf := func(runtime, path, checksum string) artifact.Entry {
		return artifact.Entry{Kind: "skill", Runtime: runtime, Path: path, SystemSHA256: checksum}
	}
	shipped := artifact.Checksum(skill.Content())
	if err := artifact.SaveRegistry(registryPath, artifact.Registry{Entries: []artifact.Entry{
		entryOf("codex", deleted, shipped),
		entryOf("hermes", unreadable, shipped),
		entryOf("claude", current, shipped),
	}}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".roca", "config.toml"), "[features]\nartifact_refresh = true\n")

	env := &cliEnv{build: Build{Version: "v2.0.0"}}
	report, err := env.refreshManagedArtifacts(filepath.Join(home, "bin", "roca"), false)
	if err != nil {
		t.Fatalf("one unreadable artifact aborted the refresh: %v", err)
	}
	if len(report.Diverged) != 1 || report.Diverged[0].Path != deleted || !report.Diverged[0].Missing {
		t.Fatalf("a deleted artifact was not reported as deleted: %+v", report.Diverged)
	}
	if len(report.Unreadable) != 1 || report.Unreadable[0] != unreadable {
		t.Fatalf("unreadable artifacts = %v", report.Unreadable)
	}
	registry, err := artifact.LoadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if entry, _ := registry.Find("skill", "claude", current); entry.InstalledVersion != "v2.0.0" {
		t.Fatalf("a readable artifact was left unchecked behind the broken one: %+v", entry)
	}

	var out, warnings strings.Builder
	(&cliEnv{out: &out, errOut: &warnings}).renderArtifactRefresh(report)
	if !strings.Contains(warnings.String(), deleted+" was removed after La Roca registered it") {
		t.Fatalf("update called a deleted artifact an edit: %q", warnings.String())
	}
	if !strings.Contains(warnings.String(), unreadable+" could not be read") {
		t.Fatalf("the unreadable artifact was not named: %q", warnings.String())
	}
}
