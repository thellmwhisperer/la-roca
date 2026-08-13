//go:build acceptance

package acceptance

import (
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cucumber/godog"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

// Steps for the installation cycle: features 01 and 02.
//
// Still black box, and here that matters more than anywhere else: the installer
// is a shell script and the release channel is GitHub. What this file stands up
// is a real HTTPS server speaking the API's shapes, and the real `install.sh` is
// run against it with the real `curl`. A test that reimplemented the download
// would be testing itself.
//
// Two rules govern the file:
//
//   - **Every scenario gets its own copy of the binary, inside its own HOME.**
//     `roca uninstall` deletes the binary it is running from, which is exactly
//     what the contract asks for and exactly what would delete `bin/roca` out
//     from under the suite if the scenarios shared it.
//   - **The channel is the scenario's, not the machine's.** No test here reaches
//     github.com, so the suite measures this binary and not somebody's network.

// thePrefix is where the installer puts the binary, relative to the HOME. It is
// the installer's own default and it is written here too, because the steps
// have to look for the file where an operator would.
var thePrefix = []string{".local", "bin"}

// theNewVersion is the tag the channel publishes as newer than the built one.
// Its artefact is not a Go binary: the contract is that the
// bytes on the PATH changed and that the new ones answer, and the installer is
// deliberately blind to what it is installing.
const theNewVersion = "v99.9.9"

// installWorld is the scenario's release channel and what it has installed.
type installWorld struct {
	server *httptest.Server
	// repo is the owner/name this channel answers for, and token the credential
	// it demands. An empty token is a public repository.
	repo  string
	token string
	// versions maps a tag to the bytes its artefact carries.
	versions map[string][]byte
	// builtVersion is what `bin/roca --version` says, which is the version the
	// channel publishes as "the current one".
	builtVersion string
	// inode identifies the installed file, for the scenario that asks whether a
	// reinstall replaced it.
	inode uint64
	// publishNew makes `releases/latest` answer with the newer tag. A channel
	// that always published the newest version could not express "installed at
	// an earlier version".
	publishNew bool
	// slowAssets holds an asset transfer open long enough for a kill that lands
	// "halfway" to actually land halfway. A local httptest server serves the
	// bytes in microseconds, so on a fast machine the atomic swap at the end of
	// the install had already happened before the SIGKILL the scenario sends at
	// 120 ms: the kill was racing the install and losing. A held-open transfer
	// turns the race into a certainty, on every platform.
	slowAssets bool
	// prettyJSON serves the release document indented the way GitHub answers
	// it, instead of the compact form json.Encoder emits by default. It is the
	// shape the installer's asset parser exists to survive, so a scenario or a
	// standalone test flips it to pin that regression.
	prettyJSON bool
}

func registerInstallSteps(ctx *godog.ScenarioContext, m *world) {
	ctx.Given(`^a clean HOME with no trace of Roca$`, m.aCleanHome)
	ctx.Given(`^La Roca is installed but not initialized$`, m.installedNotInitialized)
	ctx.Given(`^La Roca is installed and initialized with data$`, m.installedWithData)
	ctx.Given(`^a valid credential for the release repository$`, m.aValidReleaseCredential)
	ctx.Given(`^La Roca has just been installed from the release channel$`, m.installedFromTheChannel)
	ctx.Given(`^La Roca is installed at the target version$`, m.installedFromTheChannel)
	ctx.Given(`^La Roca is installed at an earlier development build$`,
		m.installedAtAnEarlierDevelopmentBuild)
	ctx.Given(`^La Roca is installed at an earlier release version$`,
		m.installedAtAnEarlierReleaseVersion)
	ctx.Given(`^there is a regular file named "roca" in the binaries directory$`, m.aStrangersFileNamedRoca)
	ctx.Given(`^the runtime is not started$`, m.theRuntimeIsNotStarted)
	ctx.Given(`^La Roca is installed in the configurations of "([^"]*)", "([^"]*)" and "([^"]*)"$`,
		m.installedInTheConfigurationsOf)

	ctx.When(`^I run the installer for the current platform$`, m.iRunTheInstaller)
	ctx.When(`^I download the installer from the repository and pipe it to a shell$`,
		m.iPipeTheInstallerToAShell)
	ctx.When(`^I launch the installer of a new version and kill it with SIGKILL halfway$`,
		m.iKillTheInstallerHalfway)
	ctx.When(`^I run the installer of that new version again and let it finish$`,
		m.iRunTheInstallerOfTheNewVersion)
	ctx.When(`^I run the installer of that same version$`, m.iRunTheInstaller)
	ctx.When(`^I run "roca uninstall" and answer "([^"]*)" to the question about keeping data$`,
		m.iUninstallAnswering)

	ctx.Then(`^there is exactly one executable file "roca" in the binaries directory$`,
		m.exactlyOneExecutableRoca)
	ctx.Then(`^the bundled resident plugin "([^"]*)" is installed without an executable$`,
		m.bundledResidentPluginIsInstalled)
	ctx.Then(`^that file is a static binary with no third-party dynamic dependencies$`,
		m.aStaticBinary)
	ctx.Then(`^there is no Python virtual environment in the HOME$`, m.noVirtualEnvironment)
	ctx.Then(`^there is no embedded interpreter in the HOME$`, m.noEmbeddedInterpreter)
	ctx.Then(`^running "roca --version" exits with code (\d+)$`, m.versionExitsWith)
	ctx.Then(`^the output of "roca --version" contains the version and the source SHA$`,
		m.theVersionCarriesTheSHA)
	ctx.Then(`^the JSON output has "([^"]*)" pointing at a file that exists$`, m.jsonKeyIsAnExistingFile)
	ctx.Then(`^the database has not changed path$`, m.theDatabaseHasNotMoved)
	ctx.Then(`^no additional database has been created$`, m.noSecondDatabase)
	ctx.Then(`^no command has needed "--db-path" to find the database$`, m.noCommandNeededTheFlag)
	ctx.Then(`^no resident process has been started$`, m.noResidentProcess)
	ctx.Then(`^no Roca process is left alive$`, m.noResidentProcess)
	ctx.Then(`^the database still exists$`, m.theDatabaseStillExists)
	ctx.Then(`^the binary is no longer linked in the binaries directory$`, m.theBinaryIsGone)
	ctx.Then(`^the binary that was active still answers "roca --version"$`, m.theActiveBinaryStillAnswers)
	ctx.Then(`^the active version is still the previous complete one$`, m.theVersionIsStillTheBuiltOne)
	ctx.Then(`^"roca --version" reports the new version$`, m.theVersionIsTheNewOne)
	ctx.Then(`^the output names what is published and how to install it$`,
		m.theRefusalNamesThePublishedVersion)
	ctx.Then(`^no partial installation tree is left in the HOME$`, m.noPartialInstallation)
	ctx.Then(`^the active binary has not changed inode$`, m.theInodeHasNotChanged)
	ctx.Then(`^the output names the file it refuses to overwrite$`, m.itNamesTheInstalledBinary)
	ctx.Then(`^that file keeps its original content$`, m.theStrangersFileIsIntact)
	ctx.Then(`^the output contains the path of the installed binary$`, m.itNamesTheInstalledBinary)
	ctx.Then(`^the output contains the path of the link that was created$`, m.itNamesTheLink)
	ctx.Then(`^if the binaries directory is not on the PATH, the output warns about it$`,
		m.itWarnsAboutThePath)
	ctx.Then(`^the output contains the installed version$`, m.itNamesTheInstalledVersion)
	ctx.Then(`^the output lists every available command$`, m.itListsEveryCommand)
	ctx.Then(`^every check appears with its verdict$`, m.everyCheckHasItsVerdict)
	ctx.Then(`^every check whose verdict is not correct names its exact remedy$`,
		m.everyFailedCheckNamesItsRemedy)
	ctx.Then(`^the previous database and configuration are still intact$`, m.theDataSurvivedTheUpdate)
	ctx.Then(`^the MCP entries in the agent configurations still point at a binary that exists$`,
		m.theMCPEntriesStillPointSomewhere)
	ctx.Then(`^the update names how many capability proposals await$`, m.updateNamesCapabilityProposals)
	ctx.Then(`^doctor lists the open capability proposals$`, m.doctorListsCapabilityProposals)
	ctx.Then(`^no agent configuration contains a Roca entry any more$`, m.noAgentConfigMentionsRoca)
	ctx.Then(`^every agent configuration keeps the rest of its content byte for byte$`,
		m.theAgentConfigsKeptTheirOwnBytes)
	ctx.Then(`^no agent configuration file has been deleted$`, m.noAgentConfigWasDeleted)
	ctx.Then(`^no Roca artefact is left in the HOME$`, m.noRocaArtefactInTheHome)
}

func (m *world) updateNamesCapabilityProposals() error {
	output := m.last.stdout + m.last.stderr
	for _, want := range []string{"1 new capability needs a look", "roca doctor"} {
		if !strings.Contains(output, want) {
			return fmt.Errorf("update does not contain %q: %s", want, output)
		}
	}
	return nil
}

func (m *world) doctorListsCapabilityProposals() error {
	output := m.last.stdout + m.last.stderr
	for _, want := range []string{"open capability proposals", "anthropic_export_paths"} {
		if !strings.Contains(output, want) {
			return fmt.Errorf("doctor code %d does not contain %q: %s", m.last.code, want, output)
		}
	}
	return nil
}

// theReleaseVersion is the clean tag a release-stamped acceptance binary reports.
// `roca update` refuses to overwrite a build that is not a published release, so
// the scenario that exercises the whole update flow needs an artefact that IS
// one. `make build` stamps the working copy (`<sha>-dirty`), which is the other
// half of that contract and is what the refusal scenario runs.
const theReleaseVersion = "v1.0.0"

// theDevVersion is what `git describe` says about a working copy: not a clean
// release tag. The refusal scenario stamps it explicitly instead of inheriting
// whatever the checkout happens to report, because the release workflow builds
// from a clean tag and would otherwise break the scenario's premise.
const theDevVersion = "1a2b3c4-dirty"

var (
	releaseStampOnce sync.Once
	releaseStampPath string
	releaseStampErr  error
	newStampOnce     sync.Once
	newStampPath     string
	newStampErr      error

	devStampOnce sync.Once
	devStampPath string
	devStampErr  error
)

func newStampedBinary() (string, error) {
	newStampOnce.Do(func() {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			newStampErr = err
			return
		}
		out := filepath.Join(root, ".tmp", "acceptance", "roca-new-release")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			newStampErr = err
			return
		}
		build := exec.Command("go", "build",
			"-ldflags", "-X main.version="+theNewVersion, "-o", out, "./cmd/roca")
		build.Dir = root
		if output, err := build.CombinedOutput(); err != nil {
			newStampErr = fmt.Errorf("stamp the new release binary: %v: %s", err, output)
			return
		}
		newStampPath = out
	})
	return newStampPath, newStampErr
}

// releaseStampedBinary is this product's code with a release version linked in.
// It is the same source `make build` compiles; only the version differs, which is
// the one input the refusal reads.
func releaseStampedBinary() (string, error) {
	releaseStampOnce.Do(func() {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			releaseStampErr = err
			return
		}
		out := filepath.Join(root, ".tmp", "acceptance", "roca-release")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			releaseStampErr = err
			return
		}
		build := exec.Command("go", "build",
			"-ldflags", "-X main.version="+theReleaseVersion, "-o", out, "./cmd/roca")
		build.Dir = root
		if output, err := build.CombinedOutput(); err != nil {
			releaseStampErr = fmt.Errorf("stamp a release binary: %v: %s", err, output)
			return
		}
		releaseStampPath = out
	})
	return releaseStampPath, releaseStampErr
}

// devStampedBinary is the same code with an explicitly non-release version
// linked in: the refusal scenario's premise, made true by construction.
func devStampedBinary() (string, error) {
	devStampOnce.Do(func() {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			devStampErr = err
			return
		}
		out := filepath.Join(root, ".tmp", "acceptance", "roca-dev")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			devStampErr = err
			return
		}
		build := exec.Command("go", "build",
			"-ldflags", "-X main.version="+theDevVersion, "-o", out, "./cmd/roca")
		build.Dir = root
		if output, err := build.CombinedOutput(); err != nil {
			devStampErr = fmt.Errorf("stamp a dev binary: %v: %s", err, output)
			return
		}
		devStampPath = out
	})
	return devStampPath, devStampErr
}

// --- the channel ---

// theChannel stands up the scenario's release server the first time somebody
// needs it. It answers the three shapes the real flow uses: reading `install.sh`
// out of the repository's contents, reading a release, and downloading an asset.
func (m *world) theChannel() *installWorld {
	if m.install.server != nil {
		return &m.install
	}
	m.install.repo = "roca-acceptance/la-roca"
	m.install.versions = map[string][]byte{}
	m.install.builtVersion = m.builtVersion()
	m.install.versions[m.install.builtVersion] = mustRead(m.artefact())
	newBinary, err := newStampedBinary()
	if err != nil {
		panic(err)
	}
	m.install.versions[theNewVersion] = mustRead(newBinary)

	channel := &m.install
	m.install.server = httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if channel.token != "" &&
				r.Header.Get("Authorization") != "Bearer "+channel.token {
				// What a private repository does to an anonymous request, which
				// is the whole reason the authenticated route exists.
				http.NotFound(w, r)
				return
			}
			channel.serve(w, r)
		}))
	writeTLSCertificate(filepath.Join(m.home, "tls-ca.pem"), m.install.server)
	return &m.install
}

func writeTLSCertificate(path string, server *httptest.Server) {
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(path, certificate, 0o600); err != nil {
		panic(err)
	}
}

func (c *installWorld) serve(w http.ResponseWriter, r *http.Request) {
	prefix := "/repos/" + c.repo + "/"
	path := strings.TrimPrefix(r.URL.Path, prefix)

	switch {
	case path == "contents/install.sh":
		http.ServeFile(w, r, filepath.Join("..", "..", "install.sh"))
	case path == "releases/latest":
		c.writeRelease(w, r, c.newest())
	case strings.HasPrefix(path, "releases/tags/"):
		c.writeRelease(w, r, strings.TrimPrefix(path, "releases/tags/"))
	case strings.HasPrefix(r.URL.Path, "/assets/"):
		c.writeAsset(w, r, strings.TrimPrefix(r.URL.Path, "/assets/"))
	default:
		http.NotFound(w, r)
	}
}

// newest is the tag `releases/latest` answers with. It is whatever the scenario
// published last, which is how a channel behaves.
func (c *installWorld) newest() string {
	if _, published := c.versions[theNewVersion]; published && c.publishNew {
		return theNewVersion
	}
	return c.builtVersion
}

func (c *installWorld) writeRelease(w http.ResponseWriter, r *http.Request, tag string) {
	if _, published := c.versions[tag]; !published {
		http.NotFound(w, r)
		return
	}
	artefact := "roca-" + tag + "-" + thePlatform()
	doc := map[string]any{
		"tag_name": tag,
		"assets": []any{
			map[string]any{"name": artefact, "url": c.server.URL + "/assets/" + tag},
			map[string]any{"name": "checksums.txt", "url": c.server.URL + "/assets/" + tag + ".sums"},
		},
	}
	// GitHub serves the release document pretty-printed (indented across many
	// lines), and the channel's default emitter mirrors that shape only when a
	// scenario asks for it: prettyJSON is the knob that serves MarshalIndent so
	// the installer's metadata parser is exercised against the form GitHub
	// actually answers with, and not only the compact one.
	enc := json.NewEncoder(w)
	if c.prettyJSON {
		enc.SetIndent("", "  ")
	}
	enc.Encode(doc)
}

// writeAsset serves an artefact, and its checksums.txt computed over the very
// bytes it is about to serve. Computed and not written down: a fixture with a
// hard-coded digest stops measuring the verification the day the artefact
// changes.
func (c *installWorld) writeAsset(w http.ResponseWriter, r *http.Request, name string) {
	if tag, isSums := strings.CutSuffix(name, ".sums"); isSums {
		payload, published := c.versions[tag]
		if !published {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, "%x  roca-%s-%s\n", sha256.Sum256(payload), tag, thePlatform())
		return
	}
	payload, published := c.versions[name]
	if !published {
		http.NotFound(w, r)
		return
	}
	if c.slowAssets {
		// Hold the connection open past the kill window, then send the bytes.
		// The bytes still arrive for a run that is allowed to finish.
		time.Sleep(400 * time.Millisecond)
	}
	w.Write(payload)
}

func (m *world) closeTheChannel() {
	if m.install.server != nil {
		m.install.server.Close()
	}
	m.install = installWorld{}
}

// --- worlds ---

func (m *world) aCleanHome() error {
	// The HOME is fresh by construction (one temporary directory per scenario).
	// What this step really declares is that the channel exists and that nothing
	// is installed yet, which is what the installer scenarios build on.
	m.theChannel()
	return nil
}

// installBinary puts a copy of the built binary where an operator's PATH would
// find it, inside this scenario's HOME.
//
// A copy and not the built file itself: `roca uninstall` deletes the binary it
// runs from, which is the contract, and sharing one file across scenarios would
// have the suite delete its own build halfway through.
func (m *world) installBinary() error {
	if m.installed != "" {
		return nil
	}
	target := theInstalledBinary(m.home)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(target, mustRead(m.artefact()), 0o755); err != nil {
		return err
	}
	m.installed = target
	return nil
}

func (m *world) installedNotInitialized() error {
	m.theChannel()
	return m.installBinary()
}

func (m *world) installedWithData() error {
	if err := m.installBinary(); err != nil {
		return err
	}
	if err := m.installedAndInitialized(); err != nil {
		return err
	}
	if _, err := m.run("roca store --layer project --content 'the installation cycle anchor'"); err != nil {
		return err
	}
	m.memories++
	return nil
}

func (m *world) aValidReleaseCredential() error {
	channel := m.theChannel()
	channel.token = "ghp-roca-acceptance-RELEASE-TOKEN"
	return nil
}

func (m *world) installedFromTheChannel() error {
	m.theChannel()
	if err := m.iRunTheInstaller(); err != nil {
		return err
	}
	// This checks whether reinstalling the same version replaced the file or
	// recognized it. The answer is the inode, and it has to be read before the
	// second run and not after it.
	return m.rememberTheInode()
}

// installedAtAnEarlierVersion: the built binary is what is installed, with its
// database and its configuration already there, and the channel publishes a
// newer version. The data has to exist
// beforehand for "still intact" to mean anything.
// artefact is the binary this scenario installs: `make build`'s own by default,
// the release-stamped one for the scenario that needs a published version, and
// the dev-stamped one for the scenario that pins the refusal.
func (m *world) artefact() string {
	if m.releaseStamped != "" {
		return m.releaseStamped
	}
	if m.devStamped != "" {
		return m.devStamped
	}
	return m.binary
}

// installedAtAnEarlierReleaseVersion is the update flow's premise: what is
// installed is a real release, older than what the channel publishes.
func (m *world) installedAtAnEarlierReleaseVersion() error {
	stamped, err := releaseStampedBinary()
	if err != nil {
		return err
	}
	m.releaseStamped = stamped
	return m.installedAtAnEarlierVersion()
}

// installedAtAnEarlierDevelopmentBuild is the refusal scenario's premise: what
// is installed is explicitly NOT a release, whatever the working copy reports.
func (m *world) installedAtAnEarlierDevelopmentBuild() error {
	stamped, err := devStampedBinary()
	if err != nil {
		return err
	}
	m.devStamped = stamped
	return m.installedAtAnEarlierVersion()
}

func (m *world) installedAtAnEarlierVersion() error {
	channel := m.theChannel()
	if err := m.installedWithData(); err != nil {
		return err
	}
	channel.publishNew = true
	return m.rememberTheData()
}

// rememberTheData records what the operator had before the update: how many
// memories the database holds and the exact bytes of the configuration. Both
// have to be there afterwards, and "the database file still exists" is not the
// same claim as "the data is still in it".
func (m *world) rememberTheData() error {
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&m.memories); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(m.home, ".roca", "config.toml"))
	if err == nil {
		m.configBefore = string(body)
	}
	return nil
}

func (m *world) aStrangersFileNamedRoca() error {
	m.theChannel()
	path := theInstalledBinary(m.home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(theStrangersContent), 0o755)
}

const theStrangersContent = "#!/bin/sh\necho this is not la roca\n"

// theRuntimeIsNotStarted checks that a command leaves no resident process; v1
// has no daemon.
func (m *world) theRuntimeIsNotStarted() error { return m.noResidentProcess() }

func (m *world) installedInTheConfigurationsOf(first, second, third string) error {
	m.agentConfigsBefore = map[string]string{}
	for _, runtime := range []string{first, second, third} {
		path, err := m.agentConfigPath(runtime)
		if err != nil {
			return err
		}
		// Content of the operator's own, written BEFORE Roca arrives. That is
		// what uninstall restores byte for byte, and a snapshot taken after
		// the install would be asking for Roca's own entry back.
		if err := m.writeTheOperatorsConfig(runtime, path); err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read the configuration of %s: %w", runtime, err)
		}
		m.agentConfigsBefore[path] = string(body)
	}
	for _, runtime := range []string{first, second, third} {
		if err := m.mustRun("roca mcp install " + runtime); err != nil {
			return err
		}
	}
	return nil
}

// writeTheOperatorsConfig plants a configuration with a neighbour of the
// operator's inside it, in the shape each runtime speaks. A file Roca created
// from nothing would prove nothing about leaving somebody's bytes alone.
func (m *world) writeTheOperatorsConfig(runtime, path string) error {
	bodies := map[string]string{
		"codex":    "# the operator wrote this\n[mcp_servers.weather]\ncommand = \"weather-mcp\"\nargs = [\"--stdio\"]\n",
		"claude":   "{\n  \"mcpServers\": {\n    \"weather\": {\n      \"command\": \"weather-mcp\"\n    }\n  }\n}\n",
		"opencode": "{\n  // the operator wrote this\n  \"mcp\": {\n    \"weather\": {\n      \"type\": \"local\",\n      \"command\": [\"weather-mcp\"]\n    }\n  }\n}\n",
	}
	body, known := bodies[runtime]
	if !known {
		return fmt.Errorf("I do not know what an operator's %s configuration looks like", runtime)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o600)
}

// --- running the installer ---

func (m *world) iRunTheInstaller() error { return m.installerRun() }

func (m *world) iRunTheInstallerOfTheNewVersion() error {
	m.install.publishNew = true
	// The completing run is allowed to finish, so the channel serves it fast.
	m.install.slowAssets = false
	return m.installerRun()
}

// iPipeTheInstallerToAShell is the operator's own one-liner: the script is
// downloaded through the authenticated contents API and piped to a shell. It is
// run through a real `curl | sh` because that pipe is what the operator types
// and what nobody had tested.
func (m *world) iPipeTheInstallerToAShell() error {
	channel := m.theChannel()
	// The credential is included on both sides of the pipe: curl needs it to read
	// a script from a private repository, and
	// the shell needs it to download the release the script then asks for.
	line := fmt.Sprintf(
		`curl -fsSL -H "Authorization: Bearer %s" -H "Accept: application/vnd.github.raw" `+
			`"%s/repos/%s/contents/install.sh" | GITHUB_TOKEN="%s" sh -s -- --repo "%s" --api "%s"`,
		channel.token, channel.server.URL, channel.repo,
		channel.token, channel.repo, channel.server.URL)
	return m.shell(line)
}

// installerRun runs the real script with the real shell against the scenario's
// own channel.
func (m *world) installerRun() error {
	channel := m.theChannel()
	return m.shell(fmt.Sprintf(`sh %q --repo %q --api %q`,
		theInstallerPath(), channel.repo, channel.server.URL))
}

// iKillTheInstallerHalfway is requirement I2, measured the way it was measured
// on the Mini: SIGKILL, which skips the script's cleanup trap and leaves
// whatever was staged behind.
func (m *world) iKillTheInstallerHalfway() error {
	channel := m.theChannel()
	channel.publishNew = true
	// Hold the asset transfer open so the kill below lands during the download
	// and not after the atomic swap at the end. See installWorld.slowAssets.
	channel.slowAssets = true
	if err := m.rememberTheInode(); err != nil {
		return err
	}

	command := exec.Command("sh", theInstallerPath(),
		"--repo", channel.repo, "--api", channel.server.URL)
	command.Env = m.environment()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return err
	}
	// Long enough to be past the release query and into the download (which the
	// channel is holding open), short enough that the move at the end has
	// certainly not happened.
	time.Sleep(120 * time.Millisecond)
	syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	command.Wait()

	m.previous = m.last
	m.last = run{command: "installer killed", code: -1}
	m.everything = append(m.everything, m.last)
	return nil
}

// iUninstallAnswering sends the operator's answer to the interactive uninstall.
func (m *world) iUninstallAnswering(answer string) error {
	command := exec.Command(m.binaryPath(), "uninstall")
	command.Stdin = strings.NewReader(answer + "\n")
	return m.record("roca uninstall <"+answer, command)
}

// shell runs one command line through /bin/sh with the scenario's environment.
// The installer is a shell script and the one-liner is a pipe, so
// what these scenarios measure is what a shell does with them.
func (m *world) shell(line string) error {
	if err := m.record(line, exec.Command("sh", "-c", line)); err != nil {
		return err
	}
	// Whatever the installer put on the PATH is what the following steps run.
	if installed := theInstalledBinary(m.home); exists(installed) {
		m.installed = installed
	}
	return nil
}

// theBinariesDirectory is where the installer puts the binary under one HOME,
// and theInstalledBinary is the file inside it. Both are written once because
// eight steps ask about that path and a second spelling of it would be a step
// looking in the wrong place.
func theBinariesDirectory(home string) string {
	return filepath.Join(append([]string{home}, thePrefix...)...)
}

func theInstalledBinary(home string) string {
	return filepath.Join(theBinariesDirectory(home), "roca")
}

// --- assertions ---

func (m *world) exactlyOneExecutableRoca() error {
	directory := theBinariesDirectory(m.home)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read the binaries directory %s: %w", directory, err)
	}
	var executables []string
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		executables = append(executables, entry.Name())
	}
	if len(executables) != 1 || executables[0] != "roca" {
		return fmt.Errorf("the binaries directory carries %v, want exactly [roca]", executables)
	}
	return nil
}

func (m *world) bundledResidentPluginIsInstalled(name string) error {
	directory := filepath.Join(m.home, ".roca", "plugins", name)
	manifest, err := plugininstall.ReadManifest(directory)
	if err != nil {
		return err
	}
	if manifest.Risk != plugininstall.DataOnly || manifest.Executable != "" {
		return fmt.Errorf("bundled plugin manifest = %+v, want data-only without executable", manifest)
	}
	if executable := filepath.Join(theBinariesDirectory(m.home), "roca-"+name); exists(executable) {
		return fmt.Errorf("bundled data plugin installed an executable at %s", executable)
	}
	descriptor, err := plugin.Inspect(name, directory)
	if err != nil {
		return err
	}
	if descriptor.Semantic.Attachment != plugin.AttachmentResident {
		return fmt.Errorf("bundled plugin attachment = %q, want resident",
			descriptor.Semantic.Attachment)
	}
	return nil
}

// aStaticBinary: nothing outside the platform's own libraries. A Go binary
// built with CGO_ENABLED=0 links libSystem on macOS and nothing at all on
// Linux, and libSystem is the platform, not a third party.
func (m *world) aStaticBinary() error {
	path := m.binaryPath()
	linked, err := dynamicDependencies(path)
	if err != nil {
		return err
	}
	for _, library := range linked {
		if !strings.HasPrefix(library, "/usr/lib/") && !strings.HasPrefix(library, "/System/") {
			return fmt.Errorf("%s links %s, which is not the platform's", path, library)
		}
	}
	return nil
}

func (m *world) noVirtualEnvironment() error {
	return m.nothingInTheHomeNamed("a virtual environment", "pyvenv.cfg", "activate")
}

func (m *world) noEmbeddedInterpreter() error {
	return m.nothingInTheHomeNamed("an interpreter",
		"python", "python3", "node", "ruby", "libpython3.so")
}

func (m *world) nothingInTheHomeNamed(what string, names ...string) error {
	unwanted := map[string]bool{}
	for _, name := range names {
		unwanted[name] = true
	}
	if found := m.filesInTheHome(func(name string) bool { return unwanted[name] }); len(found) > 0 {
		return fmt.Errorf("the HOME carries %s: %s", what, found[0])
	}
	return nil
}

// filesInTheHome is the one walk three steps share. They differ only in what
// they are looking for, and three copies of a WalkDir is three places to get
// the error handling of a missing directory wrong.
func (m *world) filesInTheHome(wanted func(name string) bool) []string {
	var found []string
	filepath.WalkDir(m.home, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && wanted(entry.Name()) {
			found = append(found, path)
		}
		return nil
	})
	return found
}

func (m *world) versionExitsWith(expected int) error {
	if _, err := m.run("roca --version"); err != nil {
		return err
	}
	return m.itExitsWithCode(expected)
}

func (m *world) theVersionCarriesTheSHA() error {
	if _, err := m.run("roca --version"); err != nil {
		return err
	}
	if err := m.outputContains(m.builtVersion()); err != nil {
		return err
	}
	// The SHA travels in parentheses beside the version, which is the shape
	// `roca version` prints and the shape the installer reads.
	if !strings.Contains(m.last.stdout, "(") || !strings.Contains(m.last.stdout, ")") {
		return fmt.Errorf("the version line carries no source SHA: %q", m.last.stdout)
	}
	return nil
}

func (m *world) jsonKeyIsAnExistingFile(key string) error {
	document, err := m.json()
	if err != nil {
		return err
	}
	value, found := lookup(document, key)
	if !found {
		return fmt.Errorf("the JSON output has no %q: %v", key, keys(document))
	}
	path := fmt.Sprint(value)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%s = %q and there is no file there: %w", key, path, err)
	}
	return nil
}

func (m *world) theDatabaseHasNotMoved() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	previous := map[string]any{}
	if err := json.Unmarshal([]byte(m.previous.stdout), &previous); err != nil {
		return fmt.Errorf("the previous output is not JSON: %w", err)
	}
	if document["db_path"] != previous["db_path"] {
		return fmt.Errorf("the database moved from %v to %v",
			previous["db_path"], document["db_path"])
	}
	return nil
}

func (m *world) noSecondDatabase() error {
	found := m.filesInTheHome(func(name string) bool { return strings.HasSuffix(name, ".db") })
	if len(found) != 1 {
		return fmt.Errorf("there are %d databases in the HOME: %v", len(found), found)
	}
	return nil
}

func (m *world) noCommandNeededTheFlag() error {
	for _, executed := range m.everything {
		if strings.Contains(executed.command, "--db-path") {
			return fmt.Errorf("a command needed the flag: %q", executed.command)
		}
	}
	return nil
}

// noResidentProcess: with no daemon there is nothing to leave behind, and the
// step checks it rather than trusting the design. It looks inside the HOME
// because that is what belongs to this scenario, and a process of another
// scenario's is not this one's business.
func (m *world) noResidentProcess() error {
	output, err := exec.Command("pgrep", "-f", m.home).CombinedOutput()
	if err != nil {
		return nil // pgrep exits non-zero when it matches nothing, which is the answer
	}
	if alive := strings.TrimSpace(string(output)); alive != "" {
		return fmt.Errorf("there are processes alive over this HOME: %s", alive)
	}
	return nil
}

func (m *world) theDatabaseStillExists() error {
	if _, err := os.Stat(filepath.Join(m.home, ".roca", "roca.db")); err != nil {
		return fmt.Errorf("the database is gone: %w", err)
	}
	return nil
}

func (m *world) theBinaryIsGone() error {
	path := theInstalledBinary(m.home)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return fmt.Errorf("%s is still linked", path)
	}
	return nil
}

func (m *world) theActiveBinaryStillAnswers() error {
	return m.versionExitsWith(0)
}

func (m *world) theVersionIsStillTheBuiltOne() error {
	if _, err := m.run("roca --version"); err != nil {
		return err
	}
	if strings.Contains(m.last.stdout, theNewVersion) {
		return fmt.Errorf("the half installation replaced the active binary: %q", m.last.stdout)
	}
	return m.outputContains(m.builtVersion())
}

func (m *world) theVersionIsTheNewOne() error {
	if _, err := m.run("roca --version"); err != nil {
		return err
	}
	return m.outputContains(theNewVersion)
}

// noPartialInstallation: what a killed run staged is this product's and the
// next run removes it. Nothing of the installer's may survive its own run.
func (m *world) noPartialInstallation() error {
	leftovers := m.filesInTheHome(func(name string) bool {
		return strings.HasPrefix(name, ".roca-install.")
	})
	if len(leftovers) > 0 {
		return fmt.Errorf("the HOME carries a half installation: %v", leftovers)
	}
	return nil
}

func (m *world) rememberTheInode() error {
	path := theInstalledBinary(m.home)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("there is no installed binary at %s: %w", path, err)
	}
	m.install.inode = inodeOf(info)
	return nil
}

func (m *world) theInodeHasNotChanged() error {
	path := theInstalledBinary(m.home)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if m.install.inode == 0 {
		return fmt.Errorf("no inode was recorded before the reinstall")
	}
	if inodeOf(info) != m.install.inode {
		return fmt.Errorf("the binary was replaced: inode %d -> %d",
			m.install.inode, inodeOf(info))
	}
	return nil
}

func (m *world) theStrangersFileIsIntact() error {
	path := theInstalledBinary(m.home)
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(body) != theStrangersContent {
		return fmt.Errorf("the file was overwritten: %q", body)
	}
	return nil
}

func (m *world) itNamesTheInstalledBinary() error {
	return m.outputContains(theInstalledBinary(m.home))
}

func (m *world) itNamesTheLink() error {
	if !strings.Contains(m.last.stdout, "link:") {
		return fmt.Errorf("the output does not name the link:\n%s", m.last.stdout)
	}
	return nil
}

// itWarnsAboutThePath is a conditional in the scenario, and it is honoured as
// one: the suite's PATH does not carry the sandbox prefix, so the warning has
// to be there.
func (m *world) itWarnsAboutThePath() error {
	prefix := theBinariesDirectory(m.home)
	if strings.Contains(":"+pathOf(m.environment())+":", ":"+prefix+":") {
		return nil
	}
	return m.outputContains("is not on your PATH")
}

func (m *world) itNamesTheInstalledVersion() error {
	return m.outputContains(m.builtVersion())
}

// itListsEveryCommand checks the deliberately small menu the CLI exposes.
func (m *world) itListsEveryCommand() error {
	for _, command := range []string{
		"init", "query", "store", "teach", "ingest", "login", "doctor", "update", "uninstall",
	} {
		if !strings.Contains(m.last.stdout, command) {
			return fmt.Errorf("the help does not list %q:\n%s", command, m.last.stdout)
		}
	}
	return nil
}

// everyCheckHasItsVerdict: doctor's readable output marks every check with
// [ok] or [no], and there is at least one of them.
func (m *world) everyCheckHasItsVerdict() error {
	if !strings.Contains(m.last.stdout, "[ok]") && !strings.Contains(m.last.stdout, "[no]") {
		return fmt.Errorf("no check carries a verdict:\n%s", m.last.stdout)
	}
	for _, line := range []string{"database:", "configuration:"} {
		if !strings.Contains(m.last.stdout, line) {
			return fmt.Errorf("doctor does not report %q:\n%s", line, m.last.stdout)
		}
	}
	return nil
}

// everyFailedCheckNamesItsRemedy requires a remedy under every failed doctor
// check.
func (m *world) everyFailedCheckNamesItsRemedy() error {
	lines := strings.Split(m.last.stdout, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "[no]") {
			continue
		}
		var remedy bool
		for _, following := range lines[i+1:] {
			if strings.Contains(following, "[ok]") || strings.Contains(following, "[no]") {
				break
			}
			if strings.Contains(following, "remedy:") {
				remedy = true
				break
			}
		}
		if !remedy {
			return fmt.Errorf("the check %q fails and names no remedy:\n%s", line, m.last.stdout)
		}
	}
	return nil
}

// theDataSurvivedTheUpdate is checked over the files and not through the
// binary: after an update the binary on the PATH is the new one, and asking the
// new binary whether the old data is fine would be trusting the very thing
// under test.
func (m *world) theDataSurvivedTheUpdate() error {
	if err := m.theDatabaseStillExists(); err != nil {
		return err
	}
	db, err := m.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	var memories int
	if err := db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&memories); err != nil {
		return fmt.Errorf("the database no longer answers: %w", err)
	}
	if memories != m.memories {
		return fmt.Errorf("memories = %d after the update, want %d", memories, m.memories)
	}
	if m.configBefore == "" {
		return nil
	}
	body, err := os.ReadFile(filepath.Join(m.home, ".roca", "config.toml"))
	if err != nil {
		return fmt.Errorf("the configuration is gone: %w", err)
	}
	if string(body) != m.configBefore {
		return fmt.Errorf("the configuration changed:\n%s", body)
	}
	return nil
}

func (m *world) theMCPEntriesStillPointSomewhere() error {
	for _, runtime := range []string{"codex", "claude", "opencode"} {
		path, err := m.agentConfigPath(runtime)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !strings.Contains(string(body), "roca") {
			continue
		}
		// The entry launches the command by name, so what has to exist is the
		// binary on the PATH, which is the file the update replaced.
		if _, err := os.Stat(m.binaryPath()); err != nil {
			return fmt.Errorf("%s points at a binary that is not there: %w", runtime, err)
		}
	}
	return nil
}

func (m *world) noAgentConfigMentionsRoca() error {
	for path := range m.agentConfigsBefore {
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if strings.Contains(string(body), `"roca"`) || strings.Contains(string(body), "[mcp_servers.roca]") {
			return fmt.Errorf("%s still declares roca:\n%s", path, body)
		}
	}
	return nil
}

// theAgentConfigsKeptTheirOwnBytes compares each file with what it was before
// Roca arrived, which the world recorded when it installed the entries. Every
// line that was not Roca's has to be there, byte for byte.
func (m *world) theAgentConfigsKeptTheirOwnBytes() error {
	for path, before := range m.agentConfigsBefore {
		after, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(before, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.Contains(trimmed, "roca") {
				continue
			}
			if !strings.Contains(string(after), trimmed) {
				return fmt.Errorf("%s lost the line %q:\n%s", path, trimmed, after)
			}
		}
	}
	return nil
}

func (m *world) noAgentConfigWasDeleted() error {
	for path := range m.agentConfigsBefore {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("the configuration %s was deleted: %w", path, err)
		}
	}
	return nil
}

// noRocaArtefactInTheHome is the zero-residue check, and it looks for the thing
// an operator would look for: anything named roca anywhere under the HOME.
func (m *world) noRocaArtefactInTheHome() error {
	var found []string
	err := filepath.WalkDir(m.home, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		relative, _ := filepath.Rel(m.home, path)
		if relative == "." || strings.HasPrefix(relative, "tmp") {
			// The HOME itself is the suite's temporary directory and its own
			// name carries "roca"; the TMPDIR inside it is the suite's too.
			return nil
		}
		if strings.Contains(strings.ToLower(entry.Name()), "roca") {
			found = append(found, relative)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(found) > 0 {
		return fmt.Errorf("the HOME still carries %v", found)
	}
	return nil
}

// --- helpers ---

func (m *world) binaryPath() string {
	if m.installed != "" {
		return m.installed
	}
	return m.binary
}

// builtVersion asks the built binary what it calls itself. The channel
// publishes that same string as "the current version", so the scenarios do not
// have to know what `git describe` said on the machine that built it.
// theRefusalNamesThePublishedVersion checks the answer a build that is not a
// published release gets from `roca update`. The suite tests exactly the artefact
// `make build` produces, and in a working copy `git describe` stamps it
// `<sha>-dirty`, so this IS the answer on this machine: the updater will not
// overwrite somebody's own build, and it says what is published and how to get it.
func (m *world) theRefusalNamesThePublishedVersion() error {
	for _, want := range []string{"not a published release", "install.sh"} {
		if err := m.outputContains(want); err != nil {
			return err
		}
	}
	return nil
}

func (m *world) builtVersion() string {
	if m.install.builtVersion != "" {
		return m.install.builtVersion
	}
	output, err := exec.Command(m.artefact(), "version").CombinedOutput()
	if err != nil {
		return "dev"
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		return "dev"
	}
	return fields[1]
}

func (m *world) agentConfigPath(runtime string) (string, error) {
	switch runtime {
	case "codex":
		return filepath.Join(m.home, ".codex", "config.toml"), nil
	case "claude":
		return filepath.Join(m.home, ".claude.json"), nil
	case "opencode":
		return filepath.Join(m.home, ".config", "opencode", "opencode.json"), nil
	case "hermes":
		return filepath.Join(m.home, ".hermes", "config.yaml"), nil
	case "pi":
		return filepath.Join(m.home, ".pi", "agent", "mcp.json"), nil
	}
	return "", fmt.Errorf("I do not know where %q keeps its configuration", runtime)
}

func theInstallerPath() string {
	path, _ := filepath.Abs(filepath.Join("..", "..", "install.sh"))
	return path
}

func thePlatform() string {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	return runtime.GOOS + "-" + arch
}

// dynamicDependencies asks the platform's own tool what the binary links.
func dynamicDependencies(path string) ([]string, error) {
	tool, arguments := "ldd", []string{path}
	if runtime.GOOS == "darwin" {
		tool, arguments = "otool", []string{"-L", path}
	}
	output, err := exec.Command(tool, arguments...).CombinedOutput()
	if err != nil {
		// `ldd` on a static binary answers "not a dynamic executable" with a
		// non-zero code, which is the answer the step wants.
		return nil, nil
	}
	var libraries []string
	for _, line := range strings.Split(string(output), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
			continue
		}
		libraries = append(libraries, fields[0])
	}
	return libraries, nil
}

func pathOf(environment []string) string {
	for _, entry := range environment {
		if value, found := strings.CutPrefix(entry, "PATH="); found {
			return value
		}
	}
	return ""
}

func mustRead(path string) []byte {
	body, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return body
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// inodeOf identifies a file by what the filesystem calls it, which is how
// This checks whether a reinstall of the same version replaced the file or
// recognized it and did nothing.
func inodeOf(info os.FileInfo) uint64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return uint64(stat.Ino)
}
