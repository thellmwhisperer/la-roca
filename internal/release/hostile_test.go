package release

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The hostile half of `roca update`: the cases where the channel, the network or
// the filesystem misbehave while the operator's only working binary is on the
// line.
//
// Every one of them answers the same question in a different way: **after this
// went wrong, does the operator still have a `roca` that runs, and were they
// told the truth about why?** A refusal that names the wrong cause costs the
// afternoon the D-3 lesson already paid for once.

// A version that was never published is a 404, and so is a private repository
// read anonymously. They have opposite remedies, and the operator who typed
// `--version v9.9.9` is told to export a token they may already have exported.
func TestATagNobodyPublishedIsNotBlamedOnTheCredential(t *testing.T) {
	source := Source{API: theChannelThatKnowsNothing(t), Repo: "owner/name", Token: "a-token"}

	_, err := source.Tagged(context.Background(), "v9.9.9")
	if err == nil {
		t.Fatal("a release that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "v9.9.9") {
		t.Errorf("the refusal does not name the version asked for: %v", err)
	}
	if strings.Contains(err.Error(), EnvToken) {
		t.Errorf("it sends an operator who already has a token to export one: %v", err)
	}
}

// The same 404 with no credential in hand is the private-repository case, and
// there the token IS the remedy. The two messages differ because the two
// machines differ.
func TestWithNoCredentialThe404StillNamesTheToken(t *testing.T) {
	source := Source{API: theChannelThatKnowsNothing(t), Repo: "owner/name"}

	_, err := source.Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), EnvToken) {
		t.Fatalf("err = %v: an anonymous 404 has to name the credential", err)
	}
}

// A download the network cut in half comes back as an error, and the error says
// what it was downloading and that nothing was replaced. `unexpected EOF` on its
// own is a sentence the operator cannot act on.
func TestADownloadTheNetworkCutIsReportedAsWhatItIs(t *testing.T) {
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("the first bytes of a binary and then the wire dies"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		panic(http.ErrAbortHandler)
	}))
	defer cut.Close()

	_, err := theArtefact(t, cut.URL+"/asset", 4096)
	if err == nil {
		t.Fatal("a cut download was accepted")
	}
	if !strings.Contains(err.Error(), theArtefactName) {
		t.Errorf("the failure does not name what was being downloaded: %v", err)
	}
	if !strings.Contains(err.Error(), "Nothing was replaced") {
		t.Errorf("it does not say the binary on the PATH is untouched: %v", err)
	}
}

// The one a cut connection does not report at all: a proxy that closes cleanly
// after half the bytes. There is no error to propagate, so the artefact has to
// be measured against the size the channel declared for it.
//
// Without that, the checksum catches it and blames the release. An operator
// reading "the channel published <a> and what came down is <b>" goes looking for
// a corrupt artefact, and their artefact is fine: their download was cut.
func TestAnArtefactShorterThanTheChannelDeclaredIsRefused(t *testing.T) {
	_, err := theArtefact(t, aChannelServing(t, "half"), 4096)
	if err == nil {
		t.Fatal("a truncated download was accepted as the whole artefact")
	}
	if !strings.Contains(err.Error(), "4096") || !strings.Contains(err.Error(), "4 ") {
		t.Errorf("the refusal does not name both sizes: %v", err)
	}
}

// A channel that declares no size for its asset is not a reason to refuse it:
// the checksum is the verification and the size is only what tells a cut
// download apart from a corrupt one.
func TestAnAssetWithNoDeclaredSizeIsStillDownloaded(t *testing.T) {
	body, err := theArtefact(t, aChannelServing(t, "the whole artefact"), 0)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(body) != "the whole artefact" {
		t.Fatalf("body = %q", body)
	}
}

// An update killed between staging and renaming leaves its staged file in the
// prefix, and it is chmod 755, so it is an executable file sitting next to the
// binary for ever. `install.sh` sweeps its own leftovers on the next run and
// this is the same rule in Go: what we own is deleted whenever it is there.
func TestAKilledUpdateLeavesNothingForTheNextOneToFind(t *testing.T) {
	dir, current := anInstalledBinary(t)
	orphan := filepath.Join(dir, ".roca-update-987654321")
	if err := os.WriteFile(orphan, []byte("#!/bin/sh\necho a killed update\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Swap(current, []byte("#!/bin/sh\necho new\n"), func(string) error { return nil }); err != nil {
		t.Fatalf("Swap: %v", err)
	}
	if leftovers := staged(t, dir); len(leftovers) > 0 {
		t.Fatalf("the prefix still carries %v: a killed update is never cleaned up", leftovers)
	}
}

// The last line of defence, and the one that decides whether a bad update is an
// inconvenience or a machine with no `roca` on it: when even the rollback rename
// fails, the previous binary is KEPT and the error says where it is.
//
// Deleting it there is the one thing that cannot be undone. The operator has no
// command left to run and nothing on disk to copy back.
func TestWhenEvenTheRollbackFailsThePreviousBinarySurvives(t *testing.T) {
	dir, current := anInstalledBinary(t)

	// The health check that runs once the new binary is in place turns the target
	// into a directory that cannot be renamed onto. It is the narrow shape of
	// every real cause: the path stopped being the file the swap left there.
	err := Swap(current, []byte("#!/bin/sh\necho new\n"), func(path string) error {
		if path != current {
			return nil
		}
		os.Remove(current)
		if err := os.MkdirAll(filepath.Join(current, "in the way"), 0o755); err != nil {
			return err
		}
		return fmt.Errorf("it does not answer --version in place")
	})
	if err == nil {
		t.Fatal("a binary that stopped answering after the swap was kept")
	}

	kept := previousBinaries(t, dir)
	if len(kept) == 0 {
		t.Fatal("the previous binary was deleted: the machine has no roca left on it")
	}
	if !strings.Contains(err.Error(), kept[0]) {
		t.Errorf("the failure does not say where the previous binary is: %v", err)
	}
}

// A prefix the operator cannot write to is refused before anything is
// downloaded into it, with the same promise every other refusal of this package
// makes: the binary on the PATH is exactly as it was.
func TestAPrefixWithNoWritePermissionSaysNothingWasReplaced(t *testing.T) {
	dir, current := anInstalledBinary(t)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	err := Swap(current, []byte("#!/bin/sh\necho new\n"), func(string) error { return nil })
	if err == nil {
		t.Skip("this filesystem let a read-only directory be written to")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the refusal does not name the directory: %v", err)
	}
	if !strings.Contains(err.Error(), "Nothing was replaced") {
		t.Errorf("it does not say the binary on the PATH is untouched: %v", err)
	}
}

// --- helpers ---

// theArtefactName is what the channel calls the asset in these cases. It is a
// constant because the refusals name it, and a second spelling would be a test
// asserting on a string no message carries.
const theArtefactName = "roca-v1-linux-x64"

// theChannelThatKnowsNothing is a release channel that answers 404 to
// everything, which is what both a private repository and an unpublished
// version look like from here.
func theChannelThatKnowsNothing(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(http.NotFound))
	t.Cleanup(server.Close)
	return server.URL
}

// aChannelServing is an asset endpoint that hands back exactly these bytes.
func aChannelServing(t *testing.T, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL + "/asset"
}

// theArtefact downloads one asset the channel declared `size` bytes of. Zero is
// a channel that declared nothing, which is a shape the real API produces.
func theArtefact(t *testing.T, url string, size int64) ([]byte, error) {
	t.Helper()
	return Source{Repo: "owner/name"}.Download(context.Background(),
		Asset{Name: theArtefactName, URL: url, Size: size})
}

// previousBinaries lists what the swap kept as the way back. It is a prefix
// match and not a name because how the file is spelled is this package's
// business; that there is one is the operator's.
func previousBinaries(t *testing.T, dir string) []string {
	t.Helper()
	var kept []string
	for _, name := range staged(t, dir) {
		if strings.HasSuffix(name, previousSuffix) {
			kept = append(kept, filepath.Join(dir, name))
		}
	}
	return kept
}
