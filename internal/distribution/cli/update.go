package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/reconcile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/release"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

// keyReleaseRepo is read under [defaults] and at the document root so an
// operator need not pass --repo on every update.
const keyReleaseRepo = "release_repo"

// versionCheck is how long the new binary gets to answer `--version`. A binary
// for the wrong architecture fails immediately; one that hangs is a binary that
// does not answer, and the operator is not made to wait for it.
const versionCheck = 20 * time.Second

// updateCommand replaces this binary with the latest published release.
//
// In a product that is one static file this is a small command, and the two
// things it may not get wrong are both about the operator's way back: the
// checksum is verified before anything is touched, and the previous binary is
// kept until the new one has answered from its final place.
func updateCommand(env *cliEnv) *cobra.Command {
	var repo, tag, api, target string
	var check, forceArtifacts bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Replace this binary with the latest published release",
		Long: "Asks the release channel for the newest version, downloads the artefact\n" +
			"for this platform, verifies its sha256 against checksums.txt BEFORE\n" +
			"touching anything, and swaps the binary by rename. If the new one does\n" +
			"not answer `--version`, the previous one comes back.\n\n" +
			"A private repository needs a token in " + release.EnvToken + ": the anonymous\n" +
			"route answers 404 there and no message can make that clearer than saying so.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			source, err := env.releaseSource(repo, api)
			if err != nil {
				return err
			}
			return env.update(cmd.Context(), source, tag, target, check, forceArtifacts)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "the release repository, owner/name (or "+release.EnvRepo+")")
	cmd.Flags().StringVar(&tag, "version", "", "a specific version instead of the latest")
	cmd.Flags().StringVar(&api, "api", "", "a trusted HTTPS test/mirror API base (or "+release.EnvAPI+")")
	cmd.Flags().StringVar(&target, "binary", "", "the binary to replace (default: the one running)")
	cmd.Flags().BoolVar(&check, "check", false, "report what is published without replacing anything")
	cmd.Flags().BoolVar(&forceArtifacts, "force-artifacts", false,
		"replace edited SYSTEM zones, and rewrite whole artifacts whose zone markers are broken, keeping a recovery copy")
	return cmd
}

// releaseSource resolves the channel: the flag, then the environment, then the
// configuration, and last the repository this product publishes from. The
// credential only ever comes from the environment, so that no output and no
// config file of this product ever holds one.
func (env *cliEnv) releaseSource(repo, api string) (release.Source, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return release.Source{}, err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return release.Source{}, err
	}
	source := release.Source{
		API: firstNonEmpty(api, os.Getenv(release.EnvAPI)),
		Repo: firstNonEmpty(repo, os.Getenv(release.EnvRepo),
			file.Default(keyReleaseRepo), release.DefaultRepo),
		Token:            os.Getenv(release.EnvToken),
		ReleaseRedirects: file.Features.ReleaseRedirects,
	}
	if err := release.ValidateMirror(source.API, source.Repo); err != nil {
		return release.Source{}, err
	}
	return source, nil
}

// releaseTag is what a published version looks like: `v1.2.3` or `1.2.3`, and
// nothing after it.
var releaseTag = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

// isReleaseBuild says whether the running binary IS a published release.
//
// `git describe` stamps a working copy as `v0.1.0-5-gabc1234`, `v0.1.0-dirty` or
// a bare commit, and `dev` when there is no tag at all. None of those equals the
// tag they would be compared against, so every one of them read as out of date
// and the updater overwrote the operator's own build with a release.
func isReleaseBuild(version string) bool {
	return releaseTag.MatchString(strings.TrimSpace(version))
}

// refuseSelfReplacement is the answer for a build that is not a release: name what
// is running, name what is published, and name the way to install it that does not
// overwrite the operator's own binary.
func (env *cliEnv) refuseSelfReplacement(latest string) error {
	env.code = ExitError
	return fmt.Errorf(
		"this build is %s, which is not a published release, so `roca update` will "+
			"not replace it. %s is published: install it with install.sh, or build "+
			"from a clean release tag",
		orDash(env.build.Version), latest)
}

// update is the whole flow, in the order that keeps a working binary on the
// machine at every step.
func (env *cliEnv) update(ctx context.Context, source release.Source,
	tag, target string, checkOnly, forceArtifacts bool) error {

	published, err := theRelease(ctx, source, tag)
	if err != nil {
		return err
	}
	platform, err := release.Platform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	current := env.build.Version
	// The refusal comes before any comparison with the published tag: a working
	// copy is not "out of date", it is simply not a release.
	if !checkOnly && !isReleaseBuild(current) {
		return env.refuseSelfReplacement(published.Tag)
	}
	if published.Tag == current {
		if !checkOnly {
			document := map[string]any{
				"updated": false, "version": current, "latest": published.Tag,
				"reason": "already at the latest version",
			}
			if installed, pathErr := binaryToReplace(target); pathErr == nil {
				if artifacts, refreshErr := env.refreshManagedArtifacts(installed, forceArtifacts); refreshErr == nil {
					document["artifacts"] = artifacts
				} else {
					fmt.Fprintf(env.errOut, "warning: registered artifacts could not be checked: %v\n", refreshErr)
				}
			}
			return env.reportUpdate(document, lenOrZero(env.openCapabilityProposals()),
				"roca %s is already the latest version", current)
		}
		return env.report(map[string]any{
			"updated": false, "version": current, "latest": published.Tag,
			"reason": "already at the latest version",
		}, "roca %s is already the latest version", current)
	}
	if checkOnly {
		return env.report(map[string]any{
			"updated": false, "version": current, "latest": published.Tag,
			"reason": "a newer version is published",
		}, "roca %s is installed; %s is published. Run `roca update` to replace it",
			current, published.Tag)
	}

	artefact := release.ArtefactName(published.Tag, platform)
	asset, found := published.Asset(artefact)
	if !found {
		return fmt.Errorf("release %s publishes no artefact %s for this platform",
			published.Tag, artefact)
	}
	sums, found := published.Asset("checksums.txt")
	if !found {
		return fmt.Errorf(
			"release %s publishes no checksums.txt: nothing is installed unverified",
			published.Tag)
	}
	if err := source.ValidateAsset(asset.URL); err != nil {
		return err
	}
	if err := source.ValidateAsset(sums.URL); err != nil {
		return err
	}

	payload, err := source.Download(ctx, asset)
	if err != nil {
		return err
	}
	checksums, err := source.Download(ctx, sums)
	if err != nil {
		return err
	}
	// Before anything on disk is touched. A binary that runs is the operator's
	// only way back and it is not risked on a download.
	if err := release.Verify(payload, string(checksums), asset.Name); err != nil {
		return err
	}
	binary, err := release.Unwrap(payload, asset.Name)
	if err != nil {
		return err
	}

	installed, err := binaryToReplace(target)
	if err != nil {
		return err
	}
	if err := release.Swap(installed, binary,
		releaseReadiness(ctx, installed, published.Tag)); err != nil {
		return err
	}
	paths, pathErr := env.resolvePaths()
	var artifacts artifactRefreshReport
	var artifactErr error
	if pathErr == nil {
		artifacts, artifactErr = artifactRefreshFromBinary(ctx, installed, paths.DB, forceArtifacts)
		if artifactErr != nil {
			fmt.Fprintf(env.errOut, "warning: the new binary could not refresh registered artifacts: %v\n", artifactErr)
		}
	}
	pending, countErr := 0, pathErr
	if pathErr == nil {
		pending, countErr = capabilityCountFromBinary(ctx, installed, paths.DB)
	}
	if countErr != nil {
		pending = lenOrZero(env.openCapabilityProposals())
		fmt.Fprintf(env.errOut, "warning: the new binary could not count capability proposals: %v\n", countErr)
	}
	document := map[string]any{
		"updated": true, "version": published.Tag, "previous": current,
		"binary": installed,
	}
	if pathErr == nil && artifactErr == nil {
		document["artifacts"] = artifacts
	}
	return env.reportUpdate(document, pending,
		"roca %s installed at %s (was %s)", published.Tag, installed, current)
}

func releaseReadiness(ctx context.Context, installed, tag string) func(string) error {
	return func(path string) error {
		if err := answersItsVersion(path); err != nil {
			return err
		}
		// Swap checks the staged binary first. It has no authority to install into
		// the live home until it occupies the final path and can still be rolled back.
		if filepath.Clean(path) != filepath.Clean(installed) {
			return nil
		}
		command := exec.CommandContext(ctx, path, "_install-bundled-plugins", "--json")
		command.Env = append(os.Environ(), envRocaPrefix+"="+filepath.Dir(installed))
		output, err := command.CombinedOutput()
		if err == nil || strings.Contains(string(output), "unknown command") {
			return nil
		}
		return fmt.Errorf("roca %s bundled plugins could not be placed: %w: %s",
			tag, err, strings.TrimSpace(string(output)))
	}
}

func lenOrZero(entries []reconcile.Entry, err error) int {
	if err != nil {
		return 0
	}
	return len(entries)
}

func (env *cliEnv) reportUpdate(document map[string]any, pending int,
	format string, args ...any) error {
	document["capability_proposals"] = pending
	if env.json {
		return env.printJSON(document)
	}
	env.print(format, args...)
	if artifacts, ok := document["artifacts"].(artifactRefreshReport); ok {
		env.renderArtifactRefresh(artifacts)
	}
	env.print("%s", capabilityCountLine(pending))
	return nil
}

const forceArtifactRefresh = "roca update --force-artifacts"

func (env *cliEnv) renderArtifactRefresh(report artifactRefreshReport) {
	if report.Enabled {
		env.print("agent artifacts: %d refreshed; %d outdated", report.Refreshed, report.Outdated)
	} else {
		env.print("agent artifacts: automatic refresh is off (features.artifact_refresh); %d outdated", report.Outdated)
	}
	for _, artifact := range report.Diverged {
		fmt.Fprintf(env.errOut, "warning: %s\n",
			divergedArtifactWarning(artifact.Path, forceArtifactRefresh,
				artifact.Missing, artifact.Unregistered))
	}
	for _, failure := range report.Failed {
		remedy := ""
		if failure.Repairable {
			remedy = fmt.Sprintf("; run `%s` to replace it, keeping the file in its recovery copy",
				forceArtifactRefresh)
		}
		fmt.Fprintf(env.errOut, "warning: %s%s\n", failure.Reason, remedy)
	}
	for _, backup := range report.Backups {
		env.print("agent artifact: replaced content kept at %s", backup)
	}
	for _, proposal := range report.Proposals {
		env.print("agent artifact available: run `%s`", proposal)
	}
}

func artifactRefreshFromBinary(ctx context.Context, binary, dbPath string,
	force bool) (artifactRefreshReport, error) {
	args := []string{"_artifacts", "--json", "--db-path", dbPath, "--executable", binary}
	if force {
		args = append(args, "--force")
	}
	output, err := exec.CommandContext(ctx, binary, args...).Output()
	if err != nil {
		return artifactRefreshReport{}, err
	}
	var report artifactRefreshReport
	if err := json.Unmarshal(output, &report); err != nil {
		return report, fmt.Errorf("decode artifact refresh: %w", err)
	}
	return report, nil
}

func capabilityCountFromBinary(ctx context.Context, binary, dbPath string) (int, error) {
	command := capabilityCountCommand(ctx, binary, dbPath)
	output, err := command.Output()
	if err != nil {
		return 0, err
	}
	return decodeCapabilityCount(output)
}

func capabilityCountCommand(ctx context.Context, binary, dbPath string) *exec.Cmd {
	return exec.CommandContext(ctx, binary, "_capabilities", "--json", "--db-path", dbPath)
}

func decodeCapabilityCount(output []byte) (int, error) {
	var result struct {
		Pending *int `json:"pending"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return 0, fmt.Errorf("decode capability count: %w", err)
	}
	if result.Pending == nil {
		return 0, fmt.Errorf("decode capability count: missing pending")
	}
	return *result.Pending, nil
}

func theRelease(ctx context.Context, source release.Source, tag string) (release.Release, error) {
	if tag != "" {
		return source.Tagged(ctx, tag)
	}
	return source.Latest(ctx)
}

// answersItsVersion is what "the new binary works" means here: it runs and it
// answers `--version`. It is the same check the operator would type, which is
// why it is the one that decides whether the swap stands.
func answersItsVersion(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), versionCheck)
	defer cancel()

	output, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// binaryToReplace is the file on the PATH, resolved through any symlink so that
// the swap replaces the real file and does not turn a link into a binary.
func binaryToReplace(declared string) (string, error) {
	path, err := declared, error(nil)
	if path == "" {
		if path, err = os.Executable(); err != nil {
			return "", fmt.Errorf(
				"I cannot tell which binary is running: name it with --binary")
		}
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return path, nil
}

// report prints one outcome in both shapes. Every command of this surface
// answers --json when asked, and update is the one an operator scripts.
func (env *cliEnv) report(document map[string]any, format string, args ...any) error {
	env.capture(document)
	if env.json {
		return env.printJSON(document)
	}
	env.print(format, args...)
	return nil
}
