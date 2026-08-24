package plugininstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/release"
)

// Known plugin-archive platform suffixes, matching release.Platform. Used to
// tell "this repo publishes plugin archives, just not for this machine" from
// "this repo has never published a plugin archive".
var pluginArchivePlatforms = []string{
	"darwin-arm64", "linux-x64", "linux-arm64", "windows-x64",
}

func (r Resolver) resolvePublishedRelease(
	ctx context.Context, reference, scratchRoot string,
) (Resolved, func(), error, bool) {
	source := release.Source{API: r.API, Repo: reference, Token: r.Token, HTTP: r.HTTP}
	found, err := source.Latest(ctx)
	if err != nil {
		if releaseNotFound(err) {
			return Resolved{}, nil, nil, false
		}
		return Resolved{}, func() {}, err, true
	}
	if !releasePublishesPluginArchive(found) {
		// A tag with no platform archives is not a plugin release. Data-only
		// plugins still install from the tree; that fallback is documented.
		return Resolved{}, nil, nil, false
	}
	platform, err := release.Platform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return Resolved{}, func() {}, err, true
	}
	asset, ok := pluginReleaseArchive(found, reference, platform)
	if !ok {
		return Resolved{}, func() {}, fmt.Errorf(
			"release %s publishes no plugin archive for %s", found.Tag, platform), true
	}
	sums, ok := found.Asset("checksums.txt")
	if !ok {
		return Resolved{}, func() {}, fmt.Errorf(
			"release %s publishes no checksums.txt: nothing is installed unverified",
			found.Tag), true
	}
	if err := source.ValidateAsset(asset.URL); err != nil {
		return Resolved{}, func() {}, err, true
	}
	if err := source.ValidateAsset(sums.URL); err != nil {
		return Resolved{}, func() {}, err, true
	}
	payload, err := source.Download(ctx, asset)
	if err != nil {
		return Resolved{}, func() {}, err, true
	}
	checksums, err := source.Download(ctx, sums)
	if err != nil {
		return Resolved{}, func() {}, err, true
	}
	if err := release.Verify(payload, string(checksums), asset.Name); err != nil {
		return Resolved{}, func() {}, err, true
	}
	directory, cleanup, err := extractArchiveBytes(payload, scratchRoot)
	if err != nil {
		return Resolved{}, func() {}, fmt.Errorf("extract plugin release archive %s: %w",
			asset.Name, err), true
	}
	return Resolved{Reference: reference, Directory: directory}, cleanup, nil, true
}

// refuseExecutableTreeFallback enforces the owner/repo tree fallback contract:
// only a data-only package may install from a repository tree when no plugin
// release archive was published.
func (r Resolver) refuseExecutableTreeFallback(reference, directory string) error {
	candidate, err := Inspect(reference, directory)
	if err != nil {
		return err
	}
	if candidate.Risk == DataOnly {
		return nil
	}
	message := fmt.Sprintf(
		"%s published no plugin release archive; the repository tree is only a fallback for a release-less data-only plugin",
		reference)
	if strings.TrimSpace(r.Token) == "" {
		message += "; if the repository is private, export " + release.EnvToken +
			" with a token that can read its releases"
	}
	return errors.New(message)
}

func extractArchiveBytes(payload []byte, scratchRoot string) (string, func(), error) {
	directory, cleanup, err := newExtractDir(scratchRoot)
	if err != nil {
		return "", func() {}, err
	}
	if err := extractTarGzip(bytes.NewReader(payload), directory); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return directory, cleanup, nil
}

func pluginReleaseArchive(found release.Release, repo, platform string) (release.Asset, bool) {
	for _, name := range pluginArtefactNames(repo, found.Tag, platform) {
		if asset, ok := found.Asset(name); ok {
			return asset, true
		}
	}
	suffixes := []string{"-" + platform + ".tar.gz", "-" + platform + ".tgz"}
	var matches []release.Asset
	for _, asset := range found.Assets {
		for _, suffix := range suffixes {
			if strings.HasSuffix(asset.Name, suffix) {
				matches = append(matches, asset)
				break
			}
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	repoName := repositoryName(repo)
	for _, asset := range matches {
		if strings.HasPrefix(asset.Name, repoName+"-") {
			return asset, true
		}
	}
	return release.Asset{}, false
}

func pluginArtefactNames(repo, tag, platform string) []string {
	name := repositoryName(repo)
	version := strings.TrimPrefix(tag, "v")
	names := []string{
		name + "-" + tag + "-" + platform + ".tar.gz",
		name + "-" + version + "-" + platform + ".tar.gz",
	}
	if tag != version {
		names = append(names, name+"-v"+version+"-"+platform+".tar.gz")
	}
	return names
}

func repositoryName(repo string) string {
	_, name, ok := strings.Cut(repo, "/")
	if !ok || name == "" {
		return repo
	}
	return name
}

func releasePublishesPluginArchive(found release.Release) bool {
	for _, platform := range pluginArchivePlatforms {
		suffixes := []string{"-" + platform + ".tar.gz", "-" + platform + ".tgz"}
		for _, asset := range found.Assets {
			for _, suffix := range suffixes {
				if strings.HasSuffix(asset.Name, suffix) {
					return true
				}
			}
		}
	}
	return false
}

func releaseNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "answered 404")
}
