package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
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
	var check bool
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
			return env.update(cmd.Context(), source, tag, target, check)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "the release repository, owner/name (or "+release.EnvRepo+")")
	cmd.Flags().StringVar(&tag, "version", "", "a specific version instead of the latest")
	cmd.Flags().StringVar(&api, "api", "", "the API base URL (or "+release.EnvAPI+")")
	cmd.Flags().StringVar(&target, "binary", "", "the binary to replace (default: the one running)")
	cmd.Flags().BoolVar(&check, "check", false, "report what is published without replacing anything")
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
	return release.Source{
		API: firstNonEmpty(api, os.Getenv(release.EnvAPI)),
		Repo: firstNonEmpty(repo, os.Getenv(release.EnvRepo),
			file.Default(keyReleaseRepo), release.DefaultRepo),
		Token: os.Getenv(release.EnvToken),
	}, nil
}

// update is the whole flow, in the order that keeps a working binary on the
// machine at every step.
func (env *cliEnv) update(ctx context.Context, source release.Source,
	tag, target string, checkOnly bool) error {

	published, err := theRelease(ctx, source, tag)
	if err != nil {
		return err
	}
	platform, err := release.Platform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	current := env.build.Version
	if published.Tag == current {
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
	if err := release.Swap(installed, binary, answersItsVersion); err != nil {
		return err
	}
	return env.report(map[string]any{
		"updated": true, "version": published.Tag, "previous": current,
		"binary": installed,
	}, "roca %s installed at %s (was %s)", published.Tag, installed, current)
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
