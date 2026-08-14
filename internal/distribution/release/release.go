// Package release is `roca update`: ask the channel what the latest version is,
// verify what it hands back and put it where the operator's PATH points.
//
// In a product that is one static file, updating is replacing that file. All
// the difficulty is in the two rules the campaign paid for:
//
//   - **The checksum is verified before anything is touched.** A binary that
//     runs is the operator's only way back, and it is not risked on a download.
//   - **The swap is a rename and it is reversible.** A rename is atomic, so the
//     PATH never points at half a file; and the previous binary is kept until
//     the new one has answered from its final place, so a build that runs from
//     a temporary directory and not from the prefix does not leave a machine
//     with no `roca` on it.
//
// The private-repo route is not an option either: this repository is
// private, the anonymous one-liner gives 404 there, and the real path is the
// authenticated API. The credential travels in the header and
// in no output.
package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// DefaultAPI is GitHub's API. It is a field and not a constant so that a test
// can point the whole command at a server of its own, which is the only way to
// measure this without a network.
const DefaultAPI = "https://api.github.com"

// EnvRepo and EnvAPI are what an operator sets once instead of passing --repo
// on every update. EnvToken is the credential the private route needs.
const (
	EnvRepo  = "ROCA_REPO"
	EnvAPI   = "ROCA_GITHUB_API"
	EnvToken = "GITHUB_TOKEN"
)

// BinaryName is what the artefact carries inside and what ends up on the PATH.
const BinaryName = "roca"

// DefaultRepo is the channel this product publishes from. It is the last
// fallback of the CLI's resolution and never a default of Source: a caller that
// builds a Source without naming a repository is asking for a refusal that says
// which flag names one, not for a request to somebody else's releases.
const DefaultRepo = "thellmwhisperer/la-roca"

// exeSuffix is what Windows needs to run a file at all. It is the one place the
// four artefact names are not the same shape, and `install.sh` already promises
// this exact name to the Windows operator it turns away.
const exeSuffix = ".exe"

// stagedPrefix and previousSuffix name the two files a swap makes beside the
// target. They are constants because three things read them: the swap that
// writes them, the sweep that clears what a killed run left, and the error that
// tells an operator where their previous binary went.
const (
	stagedPrefix   = ".roca-update-"
	previousSuffix = ".previous"
)

// maxArtefact bounds a download. A static Go binary is tens of megabytes; a
// gigabyte coming down this pipe is something else, and it is refused rather
// than written to the operator's disk.
const maxArtefact = 256 << 20

const maxReleaseRedirects = 3

// Source is the release channel.
type Source struct {
	// API is the base URL. Empty is DefaultAPI.
	API string
	// Repo is `owner/name`. It has no default here even though the product has
	// one in DefaultRepo: a Source built without a repository is a caller that
	// forgot, and asking some URL built out of an empty string is worse than
	// saying which flag names one. Resolving the default is the CLI's job.
	Repo  string
	Token string
	// HTTP is the client. Its zero value is a client with a bounded timeout.
	HTTP *http.Client
}

// Release is what the channel published.
type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}

// Asset is one published artefact. The URL is the API's own, not the browser
// one: the browser URL is anonymous and 404s on a private repository.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// Asset finds one by name, and by name with the tarball suffix, because whether
// the channel compresses is the channel's decision and not this command's.
func (r Release) Asset(name string) (Asset, bool) {
	for _, candidate := range r.Assets {
		if candidate.Name == name || candidate.Name == name+".tar.gz" {
			return candidate, true
		}
	}
	return Asset{}, false
}

// Platform is the artefact suffix for a target, or a refusal that names it.
//
// The four the channel builds are the four the Makefile builds, spelled the
// same way. A fifth invented here would download an artefact that is not
// there and report it as a network problem.
func Platform(goos, goarch string) (string, error) {
	platform := goos + "-" + map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
	switch platform {
	case "darwin-arm64", "linux-x64", "linux-arm64", "windows-x64":
		return platform, nil
	}
	return "", fmt.Errorf(
		"there is no published artefact for %s/%s: the release channel builds "+
			"darwin-arm64, linux-x64, linux-arm64 and windows-x64", goos, goarch)
}

// ArtefactName is the name the channel gives the artefact of one version for
// one platform. It is the contract shared by the workflow, `install.sh` and
// this command.
func ArtefactName(version, platform string) string {
	name := BinaryName + "-" + version + "-" + platform
	if strings.HasPrefix(platform, "windows") {
		return name + exeSuffix
	}
	return name
}

// Latest asks the channel for the newest release.
func (s Source) Latest(ctx context.Context) (Release, error) {
	return s.release(ctx, "releases/latest", "the latest release")
}

// Tagged asks for one specific release, which is how an operator pins or rolls
// back to a version they already know works.
func (s Source) Tagged(ctx context.Context, tag string) (Release, error) {
	return s.release(ctx, "releases/tags/"+tag, "release "+tag)
}

func (s Source) release(ctx context.Context, path, what string) (Release, error) {
	if strings.TrimSpace(s.Repo) == "" {
		return Release{}, fmt.Errorf(
			"I do not know which repository to update from: name it with --repo "+
				"owner/name, or set %s", EnvRepo)
	}
	body, err := s.get(ctx, s.base()+"/repos/"+s.Repo+"/"+path,
		"application/vnd.github+json", what)
	if err != nil {
		return Release{}, err
	}
	var found Release
	if err := json.Unmarshal(body, &found); err != nil {
		return Release{}, fmt.Errorf("the channel's answer is not a release: %w", err)
	}
	if found.Tag == "" {
		return Release{}, fmt.Errorf("the channel published no version at %s/%s", s.Repo, path)
	}
	return found, nil
}

// Download fetches one asset's bytes, and refuses one that came down short.
//
// A connection the network cut reports itself; one a proxy or a load balancer
// closed cleanly after half the bytes does not, and there is no error left for
// anybody to see. What catches that second one is the size the channel declared
// for the asset: without it the checksum fires instead, and an operator reading
// "the channel published <a> and what came down is <b>" goes hunting for a
// corrupt release when their release is fine and their download was cut.
//
// A channel that declares no size is not a reason to refuse anything: the
// checksum is the verification, and the size only tells the two failures apart.
func (s Source) Download(ctx context.Context, asset Asset) ([]byte, error) {
	body, err := s.get(ctx, asset.URL, "application/octet-stream", asset.Name)
	if err != nil {
		return nil, err
	}
	if asset.Size > 0 && int64(len(body)) != asset.Size {
		return nil, fmt.Errorf(
			"%s came down %d bytes and the channel declared %d: the transfer was "+
				"cut. Nothing was replaced", asset.Name, len(body), asset.Size)
	}
	return body, nil
}

// get asks the channel for one thing. `what` is that thing in the operator's
// words, and every failure carries it: a 404 and a cut transfer are both silent
// about which of the three requests an update makes was the one that broke.
func (s Source) get(ctx context.Context, url, accept, what string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", accept)
	if s.Token != "" {
		request.Header.Set("Authorization", "Bearer "+s.Token)
	}

	client, err := s.client()
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("ask the release channel: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotFound {
		return nil, s.notFound(what)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the release channel answered %s for %s",
			response.Status, what)
	}
	// One byte beyond the ceiling, so a payload over it is REFUSED instead of
	// truncated into a shorter artefact that then fails its checksum with a
	// misleading reason.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxArtefact+1))
	if err != nil {
		return nil, fmt.Errorf(
			"the transfer of %s did not finish: %w. Nothing was replaced", what, err)
	}
	if len(body) > maxArtefact {
		return nil, fmt.Errorf(
			"%s is larger than the %d bytes this version will download. Nothing was replaced",
			what, maxArtefact)
	}
	return body, nil
}

// notFound is the 404, and it has two readings with opposite remedies: a
// repository the request cannot see, and a version nobody published. GitHub
// answers the same code to both, so what tells them apart here is whether a
// credential travelled at all.
//
// When a token is already present, the message names what was requested and
// names what was asked for and leaves both live readings standing, because from
// this side they genuinely are both live.
func (s Source) notFound(what string) error {
	if s.Token == "" {
		return fmt.Errorf(
			"the release channel answered 404 for %s: if the repository is "+
				"private, export %s with a token that can read it", what, EnvToken)
	}
	return fmt.Errorf(
		"the release channel answered 404 for %s: the credential travelled with "+
			"the request, so either %s does not publish that or the token cannot "+
			"read it", what, s.Repo)
}

func (s Source) base() string {
	if s.API == "" {
		return DefaultAPI
	}
	return strings.TrimRight(s.API, "/")
}

// ValidateMirror limits custom release origins to explicit HTTPS mirrors and a
// repository path that can be appended without changing URL structure.
func ValidateMirror(api, repo string) error {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || !repoPart(parts[0]) || !repoPart(parts[1]) {
		return fmt.Errorf("the release repository must have the shape owner/name")
	}
	if api == "" {
		return nil
	}
	parsed, err := url.Parse(api)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" ||
		(parsed.Path != "" && parsed.Path != "/" && path.Clean(parsed.Path) != strings.TrimSuffix(parsed.Path, "/")) {
		return fmt.Errorf("the release API mirror must be an https base URL without credentials, query, or fragment")
	}
	return nil
}

func repoPart(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("_.-", char)) {
			return false
		}
	}
	return true
}

// ValidateAsset binds downloads to the selected HTTPS release origin.
func (s Source) ValidateAsset(raw string) error {
	base, baseErr := url.Parse(s.base())
	asset, assetErr := url.Parse(raw)
	if baseErr != nil || assetErr != nil || asset.Scheme != "https" || asset.Host != base.Host {
		return fmt.Errorf("the release asset URL must use the selected HTTPS release origin")
	}
	return nil
}

func (s Source) client() (*http.Client, error) {
	var client *http.Client
	if s.HTTP != nil {
		copy := *s.HTTP
		client = &copy
	} else {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	previousRedirectPolicy := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > maxReleaseRedirects {
			return fmt.Errorf("release channel refused more than %d redirects", maxReleaseRedirects)
		}
		if previousRedirectPolicy != nil {
			return previousRedirectPolicy(request, via)
		}
		return nil
	}
	if s.HTTP == nil {
		certificateFile := os.Getenv("SSL_CERT_FILE")
		if certificateFile == "" {
			return client, nil
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system certificates for the release channel: %w", err)
		}
		pem, err := os.ReadFile(certificateFile)
		if err != nil {
			return nil, fmt.Errorf("read SSL_CERT_FILE for the release channel: %w", err)
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("SSL_CERT_FILE contains no certificates for the release channel")
		}
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
	}
	return client, nil
}

// Verify checks the payload against its line of checksums.txt.
//
// It runs before anything on disk is touched, and an artefact with no line of
// its own is refused: passing it through would be verifying nothing while
// reporting that it verified.
func Verify(payload []byte, checksums, artefact string) error {
	sum := sha256.Sum256(payload)
	computed := hex.EncodeToString(sum[:])

	for _, line := range strings.Split(checksums, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || filepath.Base(strings.TrimPrefix(fields[1], "*")) != artefact {
			continue
		}
		if !strings.EqualFold(fields[0], computed) {
			return fmt.Errorf(
				"the checksum of %s does not match: the channel published %s and "+
					"what came down is %s. Nothing was replaced",
				artefact, fields[0], computed)
		}
		return nil
	}
	return fmt.Errorf("checksums.txt has no line for %s: nothing was replaced", artefact)
}

// Unwrap takes the binary out of what the channel published, whether that is a
// tarball with its licence beside it or a bare binary.
func Unwrap(payload []byte, artefact string) ([]byte, error) {
	if !strings.HasSuffix(artefact, ".tar.gz") {
		return payload, nil
	}
	zipped, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("the artefact %s is not a gzip: %w", artefact, err)
	}
	defer zipped.Close()

	archive := tar.NewReader(zipped)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read the artefact %s: %w", artefact, err)
		}
		if filepath.Base(header.Name) != BinaryName || header.Typeflag != tar.TypeReg {
			continue
		}
		return io.ReadAll(io.LimitReader(archive, maxArtefact))
	}
	return nil, fmt.Errorf("the artefact %s carries no %q inside it", artefact, BinaryName)
}

// Swap replaces the binary at `current` with `payload`, and puts the previous
// one back if the new one does not answer from its final place.
//
// The order is the contract:
//
//  1. Stage beside the target, so the rename that follows stays inside one
//     filesystem and is therefore atomic.
//  2. Ask the staged binary whether it answers. A wrong architecture or a
//     truncated download dies here, with nothing replaced.
//  3. Move the current one aside, rename the staged one into its place, and ask
//     again. Only then is the previous one removed.
//
// `answers` is handed the path to check so that the caller runs the real
// binary; this package does not decide what "answering" means.
func Swap(current string, payload []byte, answers func(path string) error) error {
	directory := filepath.Dir(current)
	sweepStaged(directory)

	staged, err := os.CreateTemp(directory, stagedPrefix+"*")
	if err != nil {
		return fmt.Errorf(
			"stage the new binary in %s: %w. Nothing was replaced", directory, err)
	}
	stagedPath := staged.Name()
	defer os.Remove(stagedPath)

	if _, err := staged.Write(payload); err != nil {
		staged.Close()
		return fmt.Errorf("write the new binary: %w", err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("write the new binary: %w", err)
	}
	if err := os.Chmod(stagedPath, 0o755); err != nil {
		return fmt.Errorf("make the new binary executable: %w", err)
	}
	if err := answers(stagedPath); err != nil {
		return fmt.Errorf("the downloaded binary does not answer: %w. Nothing was replaced", err)
	}

	previous := stagedPath + previousSuffix
	if err := os.Rename(current, previous); err != nil {
		return fmt.Errorf("move the current binary aside: %w. Nothing was replaced", err)
	}
	if err := os.Rename(stagedPath, current); err != nil {
		return fmt.Errorf("put the new binary in place: %w%s",
			err, rollback(previous, current))
	}
	if err := answers(current); err != nil {
		os.Remove(current)
		return fmt.Errorf("the new binary stopped answering once in place: %w%s",
			err, rollback(previous, current))
	}
	os.Remove(previous)
	return nil
}

// rollback puts the previous binary back and returns what the operator is left
// with, to be appended to the error the caller is already returning.
//
// This returns state rather than a second error because another failure would
// bury the cause; "where is my binary" is not a cause.
// What this may never do is DELETE that file. With the new binary gone and the
// previous one deleted there is no `roca` left on the machine and nothing on
// disk to copy back, which turns a bad update into a reinstall.
func rollback(previous, current string) string {
	if err := os.Rename(previous, current); err == nil {
		return ". The previous one is back"
	}
	return fmt.Sprintf(
		". Your previous binary is kept at %s: put it back at %s yourself",
		previous, current)
}

// sweepStaged clears what a killed update left in the prefix.
//
// Those files are this package's, they are chmod 755, and nothing else ever
// removes them: an update interrupted between staging and renaming would leave
// an executable orphan beside the binary for ever. Owned staging files are
// deleted whenever present, and nothing else is touched.
//
// A `.previous` is not swept. It only exists when even the rollback failed, and
// then it is the one file the operator has left.
func sweepStaged(directory string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, stagedPrefix) && !strings.HasSuffix(name, previousSuffix) {
			os.Remove(filepath.Join(directory, name))
		}
	}
}
