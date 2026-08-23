package plugininstall_test

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/distribution/release"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

// An owner/repo source that publishes a platform archive is installed from
// that archive, not from the repository tree. The tree is what produced
// "declares binary but the package supplies" when the checkout had no build.
func TestOwnerRepoInstallPrefersAPublishedReleaseArchive(t *testing.T) {
	platform := releasePlatform(t)
	name, tag := "synthetic-release", "v1.0.0"
	archive := releasePluginArchive(t, name, tag, true)
	channel := newPluginReleaseChannel(t, "owner/"+name, tag, platform, archive)
	resolver := plugininstall.Resolver{
		API: channel.URL,
		CloneURL: func(string) (string, bool) {
			t.Fatal("cloned the repository tree even though a release archive was published")
			return "", false
		},
	}

	resolved, cleanup, err := resolver.Resolve(context.Background(), "owner/"+name, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if resolved.Reference != "owner/"+name {
		t.Fatalf("recorded source = %q, want the owner/repo reference so later updates re-query latest",
			resolved.Reference)
	}
	candidate, err := plugininstall.Inspect(resolved.Reference, resolved.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Executable == "" || candidate.Version != tag {
		t.Fatalf("release candidate = %+v", candidate)
	}
	if candidate.Source != "owner/"+name {
		t.Fatalf("candidate source = %q", candidate.Source)
	}
}

// After an owner/repo install, a newer published release updates in place and
// leaves custodial database bytes untouched. The operator's data is not a
// package payload.
func TestOwnerRepoUpdateMovesForwardAndPreservesCustodyBytes(t *testing.T) {
	platform := releasePlatform(t)
	name := "synthetic-release"
	first := releasePluginArchive(t, name, "v1.0.0", true)
	channel := newPluginReleaseChannel(t, "owner/"+name, "v1.0.0", platform, first)
	resolver := plugininstall.Resolver{API: channel.URL, CloneURL: refuseClone(t)}
	root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	manager := plugininstall.Manager{PluginRoot: root, BinDir: bin}

	installFromOwnerRepo(t, resolver, manager, "owner/"+name)
	dbPath := filepath.Join(root, name, "records.db")
	marker := []byte("operator custody bytes v1")
	if err := os.WriteFile(dbPath, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	before := fileDigest(t, dbPath)

	channel.set("v1.1.0", platform, releasePluginArchive(t, name, "v1.1.0", true))
	updated := resolveOwnerRepo(t, resolver, "owner/"+name)
	if updated.Version != "v1.1.0" {
		t.Fatalf("updated candidate version = %q", updated.Version)
	}
	if _, err := manager.Update(updated); err != nil {
		t.Fatal(err)
	}
	if fileDigest(t, dbPath) != before {
		t.Fatal("update rewrote custodial database bytes")
	}
	got, err := os.ReadFile(dbPath)
	if err != nil || string(got) != string(marker) {
		t.Fatalf("custody bytes = %q, err=%v", got, err)
	}
	manifest, err := plugininstall.ReadManifest(filepath.Join(root, name))
	if err != nil || manifest.Version != "v1.1.0" || manifest.Source != "owner/"+name {
		t.Fatalf("updated manifest = %+v, err=%v", manifest, err)
	}
	body, err := os.ReadFile(filepath.Join(bin, plugininstall.ExecutableName(name)))
	if err != nil || !strings.Contains(string(body), "v1.1.0") {
		t.Fatalf("updated executable = %q, err=%v", body, err)
	}
}

// Today's failure: the recorded source is a seed tree that declares a binary
// it does not ship. Update from that source still fails the same way; an
// explicit owner/repo source rebases onto the published release without
// touching custody.
func TestUpdateRebasesASeedSourceOntoPublishedReleases(t *testing.T) {
	name := "synthetic-release"
	seed := writeReleasePluginPackage(t, filepath.Join(t.TempDir(), "seed"), name, "v0.9.0", true)
	candidate, err := plugininstall.Inspect(seed, seed)
	if err != nil {
		t.Fatal(err)
	}
	root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	manager := plugininstall.Manager{PluginRoot: root, BinDir: bin}
	if _, err := manager.Install(candidate); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, name, "records.db")
	marker := []byte("seed custody must survive the rebase")
	if err := os.WriteFile(dbPath, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	before := fileDigest(t, dbPath)

	stripPluginBinary(t, seed, name)
	if _, err := plugininstall.Inspect(seed, seed); err == nil ||
		!strings.Contains(err.Error(), "declares binary") {
		t.Fatalf("seed without a binary = %v", err)
	}

	platform := releasePlatform(t)
	archive := releasePluginArchive(t, name, "v1.0.0", true)
	channel := newPluginReleaseChannel(t, "owner/"+name, "v1.0.0", platform, archive)
	resolver := plugininstall.Resolver{API: channel.URL, CloneURL: refuseClone(t)}
	rebased := resolveOwnerRepo(t, resolver, "owner/"+name)
	if _, err := manager.Update(rebased); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dbPath)
	if err != nil || string(got) != string(marker) || fileDigest(t, dbPath) != before {
		t.Fatalf("rebase rewrote custody: %q err=%v", got, err)
	}
	manifest, err := plugininstall.ReadManifest(filepath.Join(root, name))
	if err != nil || manifest.Source != "owner/"+name || manifest.Version != "v1.0.0" {
		t.Fatalf("rebased manifest = %+v, err=%v", manifest, err)
	}
}

func TestInstallOverAnExistingPluginNamesTheExactUpdateInvocation(t *testing.T) {
	source := writeReleasePluginPackage(t, filepath.Join(t.TempDir(), "pkg"), "synthetic-release", "v1.0.0", true)
	candidate, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	manager := plugininstall.Manager{
		PluginRoot: filepath.Join(t.TempDir(), "plugins"),
		BinDir:     filepath.Join(t.TempDir(), "bin"),
	}
	if _, err := manager.Install(candidate); err != nil {
		t.Fatal(err)
	}
	err = manager.PreflightInstall(candidate)
	if err == nil {
		t.Fatal("a second install was accepted")
	}
	want := "run `roca plugin update synthetic-release " + candidate.Source + "`"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("already-installed error = %v, want %q", err, want)
	}
}

// A data-only plugin that has never published a release still installs from
// the repository tree. That fallback is the documented path, not a guess, and
// it is exercised end to end: install, write custodial bytes, then update from
// the same tree and confirm the operator's bytes survive the update.
func TestReleaseLessDataOnlyOwnerRepoFallsBackToTheTree(t *testing.T) {
	name := "synthetic-data"
	tree := writeReleasePluginPackage(t, filepath.Join(t.TempDir(), "tree"), name, "1.0.0", false)
	remote := gitRepoFromDirectory(t, tree)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	cloned := false
	resolver := plugininstall.Resolver{
		API: server.URL,
		CloneURL: func(reference string) (string, bool) {
			if reference != "owner/"+name {
				t.Fatalf("clone reference = %q", reference)
			}
			cloned = true
			return remote, true
		},
	}
	root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	manager := plugininstall.Manager{PluginRoot: root, BinDir: bin}

	resolved, cleanup, err := resolver.Resolve(context.Background(), "owner/"+name, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !cloned {
		t.Fatal("release-less owner/repo did not fall back to the tree")
	}
	candidate, err := plugininstall.Inspect(resolved.Reference, resolved.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Executable != "" || candidate.Risk != plugininstall.DataOnly || candidate.Version != "1.0.0" {
		t.Fatalf("tree candidate = %+v", candidate)
	}
	if _, err := manager.Install(candidate); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, name, "records.db")
	marker := []byte("data-only custody survives the tree fallback update")
	if err := os.WriteFile(dbPath, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	before := fileDigest(t, dbPath)

	cloned = false
	updatedResolved, updatedCleanup, err := resolver.Resolve(context.Background(), "owner/"+name, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer updatedCleanup()
	if !cloned {
		t.Fatal("update did not fall back to the tree again")
	}
	updated, err := plugininstall.Inspect(updatedResolved.Reference, updatedResolved.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update(updated); err != nil {
		t.Fatal(err)
	}
	if fileDigest(t, dbPath) != before {
		t.Fatal("update rewrote custodial database bytes")
	}
	got, err := os.ReadFile(dbPath)
	if err != nil || string(got) != string(marker) {
		t.Fatalf("custody bytes = %q, err=%v", got, err)
	}
}

// A data-only plugin whose repo has published a tag but no plugin archive
// (only a source tarball) still installs from the tree. A tag is not a plugin
// release until it carries a platform archive, and the tree fallback must not
// be blocked by a platform the release channel does not build.
func TestDataOnlyOwnerRepoFallsBackToTheTreeWhenTheReleaseCarriesNoPluginArchive(t *testing.T) {
	name := "synthetic-data"
	tree := writeReleasePluginPackage(t, filepath.Join(t.TempDir(), "tree"), name, "1.0.0", false)
	remote := gitRepoFromDirectory(t, tree)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/"+name+"/releases/latest" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v1.0.0",
				"assets": []any{
					map[string]any{
						"name": name + "-1.0.0-source.tar.gz",
						"url":  "http://" + r.Host + "/repos/owner/" + name + "/releases/assets/1",
						"size": 0,
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	cloned := false
	resolver := plugininstall.Resolver{
		API: server.URL,
		CloneURL: func(reference string) (string, bool) {
			if reference != "owner/"+name {
				t.Fatalf("clone reference = %q", reference)
			}
			cloned = true
			return remote, true
		},
	}
	resolved, cleanup, err := resolver.Resolve(context.Background(), "owner/"+name, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !cloned {
		t.Fatal("a tag with no plugin archive did not fall back to the tree")
	}
	candidate, err := plugininstall.Inspect(resolved.Reference, resolved.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Risk != plugininstall.DataOnly {
		t.Fatalf("tree candidate = %+v, want data-only", candidate)
	}
}

// A release-less owner/repo whose tree ships an executable must not install
// from that unverified tree: the fallback exists only for data-only plugins.
func TestOwnerRepoTreeFallbackRefusesAnExecutablePackage(t *testing.T) {
	name := "synthetic-release"
	tree := writeReleasePluginPackage(t, filepath.Join(t.TempDir(), "tree"), name, "v1.0.0", true)
	remote := gitRepoFromDirectory(t, tree)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	cloned := false
	resolver := plugininstall.Resolver{
		API: server.URL,
		CloneURL: func(reference string) (string, bool) {
			if reference != "owner/"+name {
				t.Fatalf("clone reference = %q", reference)
			}
			cloned = true
			return remote, true
		},
	}
	_, cleanup, err := resolver.Resolve(context.Background(), "owner/"+name, t.TempDir())
	if err == nil {
		cleanup()
		t.Fatal("executable tree fallback was accepted")
	}
	if !cloned {
		t.Fatal("release-less owner/repo did not fall back to the tree")
	}
	for _, needle := range []string{
		"published no plugin release archive",
		"release-less data-only plugin",
	} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("refusal error %q does not mention %q", err, needle)
		}
	}
	if !strings.Contains(err.Error(), release.EnvToken) {
		t.Errorf("refusal error %q does not mention %s for a private repository", err, release.EnvToken)
	}
}

func releasePlatform(t *testing.T) string {
	t.Helper()
	platform, err := release.Platform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	return platform
}

func refuseClone(t *testing.T) func(string) (string, bool) {
	t.Helper()
	return func(string) (string, bool) {
		t.Fatal("cloned the repository tree")
		return "", false
	}
}

func installFromOwnerRepo(t *testing.T, resolver plugininstall.Resolver, manager plugininstall.Manager, reference string) plugininstall.Candidate {
	t.Helper()
	candidate := resolveOwnerRepo(t, resolver, reference)
	if _, err := manager.Install(candidate); err != nil {
		t.Fatal(err)
	}
	return candidate
}

func resolveOwnerRepo(t *testing.T, resolver plugininstall.Resolver, reference string) plugininstall.Candidate {
	t.Helper()
	resolved, cleanup, err := resolver.Resolve(context.Background(), reference, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	candidate, err := plugininstall.Inspect(resolved.Reference, resolved.Directory)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func releasePluginArchive(t *testing.T, name, version string, withBinary bool) []byte {
	t.Helper()
	directory := writeReleasePluginPackage(t, filepath.Join(t.TempDir(), name), name, version, withBinary)
	return archivePackageRoot(t, directory)
}

func writeReleasePluginPackage(t *testing.T, directory, name, version string, withBinary bool) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := plugin.HostBinary
	if withBinary {
		binary = plugininstall.ExecutableName(name)
	}
	manifest := map[string]any{
		"schema": 1, "name": name, "version": version, "binary": binary,
		"databases": []map[string]any{
			{
				"name": "records", "path": "records.db", "alias": "synthetic_records",
				"attachment": "resident", "custody": true,
				"retention": "Keep operator records until the plugin is uninstalled.",
			},
		},
		"semantic": map[string]any{
			"databases": []map[string]any{
				{
					"database": "records", "description": "Synthetic custodial records.",
					"questions": []string{"Which synthetic records exist?"},
					"tables": []map[string]any{{
						"name": "entries", "description": "One synthetic row.",
						"columns": []string{"id", "value"},
					}},
				},
			},
		},
		"verbs":        []map[string]any{},
		"capabilities": []map[string]any{},
	}
	writePackageMetadata(t, directory, manifest)
	if err := os.Remove(filepath.Join(directory, "records.db")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	withPackageDatabase(t, filepath.Join(directory, "records.db"), func(db *sql.DB) {
		if _, err := db.Exec(`CREATE TABLE entries (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
	})
	files := []string{"plugin.json", "records.db"}
	if withBinary {
		executable := plugininstall.ExecutableName(name)
		writeFixtureFile(t, filepath.Join(directory, executable),
			[]byte("#!/bin/sh\nprintf '"+version+"\\n'\n"), 0o700)
		files = append(files, executable)
	}
	writeChecksums(t, directory, files)
	return directory
}

func stripPluginBinary(t *testing.T, directory, name string) {
	t.Helper()
	executable := plugininstall.ExecutableName(name)
	if err := os.Remove(filepath.Join(directory, executable)); err != nil {
		t.Fatal(err)
	}
	writeChecksums(t, directory, []string{"plugin.json", "records.db"})
}

func archivePackageRoot(t *testing.T, directory string) []byte {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	return makeArchive(t, func(archive *tar.Writer) {
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			body, err := os.ReadFile(filepath.Join(directory, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			mode := int64(0o600)
			if info.Mode().Perm()&0o111 != 0 {
				mode = 0o700
			}
			if err := archive.WriteHeader(&tar.Header{
				Name: "./" + entry.Name(), Mode: mode, Size: int64(len(body)),
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := archive.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func pluginArtefact(repo, tag, platform string) string {
	_, name, ok := strings.Cut(repo, "/")
	if !ok {
		name = repo
	}
	return name + "-" + tag + "-" + platform + ".tar.gz"
}

type pluginReleaseChannel struct {
	URL  string
	repo string

	mu       sync.Mutex
	tag      string
	platform string
	archive  []byte
	sums     []byte
	artefact string
}

func newPluginReleaseChannel(t *testing.T, repo, tag, platform string, archive []byte) *pluginReleaseChannel {
	t.Helper()
	channel := &pluginReleaseChannel{repo: repo}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		channel.serve(w, r)
	}))
	t.Cleanup(server.Close)
	channel.URL = server.URL
	channel.set(tag, platform, archive)
	return channel
}

func (c *pluginReleaseChannel) set(tag, platform string, archive []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tag = tag
	c.platform = platform
	c.archive = archive
	c.artefact = pluginArtefact(c.repo, tag, platform)
	sum := sha256.Sum256(archive)
	c.sums = []byte(fmt.Sprintf("%x  %s\n", sum, c.artefact))
}

func (c *pluginReleaseChannel) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	repo, tag, artefact, archive, sums := c.repo, c.tag, c.artefact, c.archive, c.sums
	c.mu.Unlock()
	switch r.URL.Path {
	case "/repos/" + repo + "/releases/latest":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": tag,
			"assets": []any{
				map[string]any{
					"name": artefact,
					"url":  c.URL + "/repos/" + repo + "/releases/assets/1",
					"size": len(archive),
				},
				map[string]any{
					"name": "checksums.txt",
					"url":  c.URL + "/repos/" + repo + "/releases/assets/2",
					"size": len(sums),
				},
			},
		})
	case "/repos/" + repo + "/releases/assets/1":
		_, _ = w.Write(archive)
	case "/repos/" + repo + "/releases/assets/2":
		_, _ = w.Write(sums)
	default:
		http.NotFound(w, r)
	}
}

func gitRepoFromDirectory(t *testing.T, directory string) string {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = directory
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	run("init")
	run("-c", "user.email=plugin@example.test", "-c", "user.name=Plugin Test", "add", ".")
	run("-c", "user.email=plugin@example.test", "-c", "user.name=Plugin Test", "commit", "-m", "seed")
	return directory
}
