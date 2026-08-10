package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The artefact name is the contract between the release channel, the installer
// and this command. All three have to spell it the same way or an update looks
// like "there is no artefact for your platform" on a platform that has one.
func TestTheArtefactIsNamedTheWayTheChannelBuildsIt(t *testing.T) {
	cases := []struct{ goos, arch, want string }{
		{"darwin", "arm64", "roca-v1.2.3-darwin-arm64"},
		{"linux", "amd64", "roca-v1.2.3-linux-x64"},
		{"linux", "arm64", "roca-v1.2.3-linux-arm64"},
		// Windows carries its extension, because without it the operating
		// system will not run the file at all. `install.sh` already promises
		// that exact name to the operator it turns away.
		{"windows", "amd64", "roca-v1.2.3-windows-x64.exe"},
	}
	for _, c := range cases {
		platform, err := Platform(c.goos, c.arch)
		if err != nil {
			t.Fatalf("%s/%s: %v", c.goos, c.arch, err)
		}
		if got := ArtefactName("v1.2.3", platform); got != c.want {
			t.Errorf("%s/%s -> %q, want %q", c.goos, c.arch, got, c.want)
		}
	}
}

// A platform with no artefact says so by name. Guessing one would download an
// artefact that cannot run and swap it in.
func TestAnUnbuiltPlatformIsRefusedByName(t *testing.T) {
	_, err := Platform("plan9", "mips")
	if err == nil {
		t.Fatal("plan9/mips was accepted")
	}
	if !strings.Contains(err.Error(), "plan9") {
		t.Errorf("the refusal does not name the platform: %v", err)
	}
}

// The checksum is verified before anything is touched, and a mismatch names
// both digests: an operator has to be able to tell a corrupt download from a
// checksums file describing a different build.
func TestAChecksumMismatchIsRefusedAndNamesBothDigests(t *testing.T) {
	payload := []byte("the artefact")
	checksums := "0000000000000000000000000000000000000000000000000000000000000000  roca-v1-linux-x64\n"

	err := Verify(payload, checksums, "roca-v1-linux-x64")
	if err == nil {
		t.Fatal("a payload whose digest does not match was accepted")
	}
	sum := sha256.Sum256(payload)
	if !strings.Contains(err.Error(), hex.EncodeToString(sum[:])) {
		t.Errorf("the refusal does not name the digest it computed: %v", err)
	}
}

// An artefact with no line of its own in checksums.txt is refused too. Passing
// it through would be verifying nothing at all.
func TestAnArtefactWithNoChecksumLineIsRefused(t *testing.T) {
	err := Verify([]byte("x"), "abc  another-artefact\n", "roca-v1-linux-x64")
	if err == nil {
		t.Fatal("an artefact absent from checksums.txt was accepted")
	}
	if !strings.Contains(err.Error(), "roca-v1-linux-x64") {
		t.Errorf("the refusal does not name the artefact: %v", err)
	}
}

func TestAMatchingChecksumPasses(t *testing.T) {
	payload := []byte("the artefact")
	sum := sha256.Sum256(payload)
	line := hex.EncodeToString(sum[:]) + "  roca-v1-linux-x64\n"
	if err := Verify(payload, "other  x\n"+line, "roca-v1-linux-x64"); err != nil {
		t.Fatalf("a matching digest was refused: %v", err)
	}
}

func TestAStandardSha256sumBinaryChecksumPasses(t *testing.T) {
	payload := []byte("the artefact")
	sum := sha256.Sum256(payload)
	line := hex.EncodeToString(sum[:]) + " *roca-v1-linux-x64\n"
	if err := Verify(payload, line, "roca-v1-linux-x64"); err != nil {
		t.Fatalf("sha256sum -b output was refused: %v", err)
	}
}

func TestReleaseAssetsStayOnTheSelectedHTTPSOrigin(t *testing.T) {
	source := Source{API: "https://mirror.example/api/v3", Repo: "owner/repo"}
	if err := source.ValidateAsset("https://mirror.example/assets/roca"); err != nil {
		t.Fatalf("same-origin HTTPS asset was refused: %v", err)
	}
	for _, raw := range []string{
		"http://mirror.example/assets/roca",
		"https://other.example/assets/roca",
	} {
		if err := source.ValidateAsset(raw); err == nil {
			t.Errorf("asset URL %q was accepted", raw)
		}
	}
}

// The channel compresses each artefact with its LICENSE and README; installation
// extracts the binary rather than storing the tarball.
func TestTheBinaryIsTakenOutOfTheTarball(t *testing.T) {
	payload := tarball(t, map[string]string{
		"LICENSE": "a licence",
		"roca":    "the binary",
		"README":  "a readme",
	})
	binary, err := Unwrap(payload, "roca-v1-linux-x64.tar.gz")
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if string(binary) != "the binary" {
		t.Fatalf("binary = %q", binary)
	}
}

// An artefact published as a bare binary is installed as it is: the format is
// the channel's decision and this command reads both.
func TestABareArtefactIsInstalledAsItIs(t *testing.T) {
	binary, err := Unwrap([]byte("the binary"), "roca-v1-linux-x64")
	if err != nil {
		t.Fatal(err)
	}
	if string(binary) != "the binary" {
		t.Fatalf("binary = %q", binary)
	}
}

// A tarball with no roca in it is refused by name instead of installing
// whatever happened to be first.
func TestATarballWithoutTheBinaryIsRefused(t *testing.T) {
	payload := tarball(t, map[string]string{"LICENSE": "a licence"})
	if _, err := Unwrap(payload, "roca-v1-linux-x64.tar.gz"); err == nil {
		t.Fatal("a tarball with no binary was accepted")
	}
}

// The swap is by rename, which is atomic: at no instant is there half a binary
// where the operator's PATH points.
func TestTheSwapLeavesTheNewBinaryInPlace(t *testing.T) {
	dir, current := anInstalledBinary(t)

	err := Swap(current, []byte("#!/bin/sh\necho new\n"), func(string) error { return nil })
	if err != nil {
		t.Fatalf("Swap: %v", err)
	}
	if body := read(t, current); !strings.Contains(body, "new") {
		t.Fatalf("the binary in place is %q", body)
	}
	if leftovers := staged(t, dir); len(leftovers) > 0 {
		t.Fatalf("the swap left %v behind", leftovers)
	}
}

// A new binary that does not answer never reaches the operator's PATH: it is
// checked while it is still staged, and the one that works stays where it is.
func TestABinaryThatDoesNotAnswerIsNeverSwappedIn(t *testing.T) {
	dir, current := anInstalledBinary(t)

	err := Swap(current, []byte("broken"), func(string) error {
		return fmt.Errorf("it does not answer --version")
	})
	if err == nil {
		t.Fatal("a binary that does not answer was installed")
	}
	if body := read(t, current); !strings.Contains(body, "old") {
		t.Fatalf("the previous binary was lost: %q", body)
	}
	if leftovers := staged(t, dir); len(leftovers) > 0 {
		t.Fatalf("the failed swap left %v behind", leftovers)
	}
}

// The rollback: the staged binary answered, the swapped-in one does not. The
// previous one comes back and the operator keeps a working command.
func TestAnUnswappableBinaryIsRolledBack(t *testing.T) {
	dir, current := anInstalledBinary(t)

	asks := 0
	err := Swap(current, []byte("#!/bin/sh\necho new\n"), func(path string) error {
		asks++
		// It answers while staged and stops answering once it is in place,
		// which is the only failure a pre-check cannot catch.
		if path == current {
			return fmt.Errorf("it does not answer --version in place")
		}
		return nil
	})
	if err == nil {
		t.Fatal("a binary that stopped answering after the swap was kept")
	}
	if asks < 2 {
		t.Fatalf("the binary was checked %d times: it has to be checked in place too", asks)
	}
	if body := read(t, current); !strings.Contains(body, "old") {
		t.Fatalf("the rollback did not restore the previous binary: %q", body)
	}
	if leftovers := staged(t, dir); len(leftovers) > 0 {
		t.Fatalf("the rollback left %v behind", leftovers)
	}
}

// The release query speaks the GitHub API and carries the credential, because
// the reference repository is private and the anonymous route gives 404.
func TestTheLatestReleaseIsReadOverTheAuthenticatedAPI(t *testing.T) {
	var sawToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawToken = r.Header.Get("Authorization")
		if r.URL.Path != "/repos/owner/name/releases/latest" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v9.9.9",
			"assets": []any{
				map[string]any{"name": "checksums.txt", "url": "http://x/1"},
				map[string]any{"name": "roca-v9.9.9-linux-x64.tar.gz", "url": "http://x/2"},
			},
		})
	}))
	defer server.Close()

	source := Source{API: server.URL, Repo: "owner/name", Token: "a-token"}
	found, err := source.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if found.Tag != "v9.9.9" {
		t.Fatalf("tag = %q", found.Tag)
	}
	if sawToken != "Bearer a-token" {
		t.Fatalf("Authorization = %q: the private route needs the credential", sawToken)
	}
	if _, ok := found.Asset("checksums.txt"); !ok {
		t.Error("checksums.txt was not found among the assets")
	}
}

// A repository nobody named is a refusal that says which flag names it, not a
// request to a URL built out of an empty string.
func TestWithNoRepositoryTheCommandSaysWhichFlagNamesIt(t *testing.T) {
	_, err := Source{API: "http://127.0.0.1:1"}.Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--repo") {
		t.Fatalf("err = %v, it does not name the flag", err)
	}
}

// --- helpers ---

func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	zipped := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(zipped)
	for name, body := range files {
		if err := archive.WriteHeader(&tar.Header{
			Name: "roca-v1-linux-x64/" + name, Mode: 0o755, Size: int64(len(body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// anInstalledBinary is the state every swap starts from: a directory with a
// working `roca` in it, which is what the operator's PATH points at.
func anInstalledBinary(t *testing.T) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "roca")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// staged lists whatever the swap left in the directory besides the binary
// itself. A swap that leaves temporary files behind is a swap that fills a
// prefix directory up over months of updates.
func staged(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, entry := range entries {
		if entry.Name() != "roca" {
			out = append(out, entry.Name())
		}
	}
	return out
}

func TestMain(m *testing.M) {
	if runtime.GOOS == "windows" {
		os.Exit(0) // the swap tests run a shell script as the binary
	}
	os.Exit(m.Run())
}
