//go:build acceptance

package acceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"github.com/thellmwhisperer/la-roca/test/testfixture"
)

// Which roca a resident answers with (#588). The fast scenario stands a fake
// vector resident in for the model; the e2e-federation ones install the
// published release and the candidate with install.sh over the frozen lab and
// answer with the pinned model.

// residentWorld is what these scenarios add to the environment: a short vector
// resident socket, the stand-in resident, and the operator's PATH.
type residentWorld struct {
	// socket is relative to the suite's directory, like the core one, so a
	// long checkout path still fits a unix socket.
	socket string
	dir    string
	// payload replaces roca-vector as the resident executable.
	payload string
	// operatorPath puts ~/.local/bin first on PATH, as on the operator's shell.
	operatorPath bool
	// queries are the runs started at the same time.
	queries []run
}

func (r *residentWorld) environment() []string {
	var environment []string
	if r.socket != "" {
		environment = append(environment, "ROCA_VECTOR_RESIDENT_SOCKET="+r.socket)
	}
	if r.payload != "" {
		environment = append(environment, "ROCA_VECTOR_RESIDENT_BINARY="+r.payload)
	}
	return environment
}

func (r *residentWorld) close() {
	if r.socket != "" {
		testfixture.KillResidents(r.socket)
	}
	_ = os.RemoveAll(r.dir)
	*r = residentWorld{}
}

func registerResidentSteps(ctx *godog.ScenarioContext, m *world) {
	ctx.Given(`^semantic search is answered by a stand-in vector resident$`, m.standInVectorResident)
	ctx.Given(`^a frozen synthetic federation lab installed by install\.sh from the release channel$`,
		func() error { return m.frozenLabInstalledByInstaller(false) })
	ctx.Given(`^a frozen synthetic federation lab installed by install\.sh at the published release$`,
		func() error { return m.frozenLabInstalledByInstaller(true) })
	ctx.Given(`^the lab is migrated and serves the cutover layout$`, m.labServesCutover)
	ctx.Given(`^an MCP session started with a PATH without ~/\.local/bin has called roca_vector_query once$`,
		m.mcpSessionCalledVectorQuery)
	ctx.Given(`^that MCP session is closed$`, func() error { m.closeThePlug(); return nil })
	ctx.When(`^the candidate is installed by install\.sh over it$`, m.candidateInstalledOverIt)
	ctx.When(`^I run "([^"]*)" with ~/\.local/bin on PATH$`, m.iRunOnTheOperatorPath)
	ctx.When(`^I run two "([^"]*)" at the same time with ~/\.local/bin on PATH$`, m.iRunTwoAtTheSameTime)
	ctx.When(`^the MCP session calls roca_vector_query "([^"]*)" again$`, func(query string) error {
		return m.callTool("roca_vector_query", map[string]any{"query": query, "k": 10})
	})
	ctx.Step(`^the stand-in vector resident was told the installed roca$`, m.standInWasToldTheInstalledRoca)
	ctx.Then(`^no resident of the home listens any more$`, m.noResidentListens)
	registerQueryListSteps(ctx, m)
	registerResidentConfigSteps(ctx, m)
	ctx.Then(`^the vector hits include the harbor lantern exchange$`, func() error {
		return harborLanternExchangeAmong(m.last.stdout)
	})
	ctx.Then(`^the MCP vector hits include the harbor lantern exchange$`, func() error {
		if err := m.theResponseIsNotAnError(); err != nil {
			return err
		}
		return harborLanternExchangeAmong(renderedText(m.plug.last))
	})
	ctx.Then(`^both queries exit 0 with "([^"]*)" among their engines$`, m.bothQueriesHaveEngine)
	ctx.Then(`^exactly one vector resident process runs for the home$`, m.exactlyOneVectorResident)
}

// shortResidentSockets gives the core and the vector resident sockets that fit,
// shared by every process of the scenario through its environment.
func (m *world) shortResidentSockets() error {
	if m.residentSocket == "" {
		socket, cleanup, err := acceptanceResident()
		if err != nil {
			return err
		}
		m.residentSocket, m.residentCleanup = socket, cleanup
	}
	if m.residents.socket != "" {
		return nil
	}
	var err error
	m.residents.dir, m.residents.socket, err = shortSocket("vres-", "resident.sock")
	return err
}

func (m *world) standInVectorResident() error {
	if err := enableVectorFeature(m.home); err != nil {
		return err
	}
	fake := filepath.Join(m.home, "tmp", "roca-vector-stand-in")
	if err := buildFakeVectorResidentAt(fake); err != nil {
		return err
	}
	m.residents.payload = fake
	return m.shortResidentSockets()
}

func (m *world) mcpSessionCalledVectorQuery() error {
	if err := m.shortResidentSockets(); err != nil {
		return err
	}
	if err := m.pathLacksLocalBin(); err != nil {
		return err
	}
	// The call answers or not depending on the build that serves it; what the
	// scenario needs is the residents it leaves running with this PATH.
	return m.callTool("roca_vector_query", map[string]any{"query": "harbor lantern", "k": 10})
}

func (m *world) standInWasToldTheInstalledRoca() error {
	if err := m.theResponseIsNotAnError(); err != nil {
		return err
	}
	var answer struct {
		Core string `json:"core"`
	}
	if err := json.Unmarshal([]byte(renderedText(m.plug.last)), &answer); err != nil {
		return fmt.Errorf("the vector answer is not JSON: %w\n%s", err, renderedText(m.plug.last))
	}
	core, err := os.Stat(answer.Core)
	if err != nil {
		return fmt.Errorf("the resident was told the core %q: %w", answer.Core, err)
	}
	installed, err := os.Stat(theInstalledBinary(m.home))
	if err != nil {
		return err
	}
	if !os.SameFile(core, installed) {
		return fmt.Errorf("the resident was told the core %s, not the installed %s", answer.Core, theInstalledBinary(m.home))
	}
	return nil
}

func (m *world) noResidentListens() error {
	for _, socket := range []string{m.residentSocket, m.residents.socket, m.residents.socket + ".current"} {
		if conn, err := net.DialTimeout("unix", socket, 200*time.Millisecond); err == nil {
			_ = conn.Close()
			return fmt.Errorf("a resident still listens on %s", socket)
		}
	}
	return nil
}

// frozenLabInstalledByInstaller is the frozen main home with roca put in
// ~/.local/bin by install.sh from the scenario's channel, which publishes the
// candidate and the release before it.
func (m *world) frozenLabInstalledByInstaller(published bool) error {
	if err := m.extractVerifiedSnapshot("main"); err != nil {
		return err
	}
	candidate := binaryVersion(m.binary)
	versions := map[string][]byte{candidate: mustRead(m.binary)}
	tag := candidate
	if published {
		release := strings.TrimSpace(os.Getenv("ROCA_PUBLISHED_BIN"))
		if release == "" {
			return fmt.Errorf("ROCA_PUBLISHED_BIN is required for the upgrade over a published release")
		}
		tag = binaryVersion(release)
		if tag == candidate {
			return fmt.Errorf("the published release and the candidate both call themselves %s", tag)
		}
		versions[tag] = mustRead(release)
	}
	m.openTheChannel(candidate, versions)
	if err := m.installerRunVersion(tag); err != nil {
		return err
	}
	if err := m.readyFrozenVectorState(); err != nil {
		return err
	}
	return m.shortResidentSockets()
}

// installerRunVersion is install.sh pinned to one tag of the scenario's channel.
func (m *world) installerRunVersion(tag string) error {
	channel := m.theChannel()
	if err := m.shell(fmt.Sprintf(`sh %q --repo %q --api %q --version %q`,
		theInstallerPath(), channel.repo, channel.server.URL, tag)); err != nil {
		return err
	}
	if m.last.code != 0 {
		return fmt.Errorf("install.sh %s exited %d: %s", tag, m.last.code, m.last.stderr+m.last.stdout)
	}
	return nil
}

func (m *world) candidateInstalledOverIt() error {
	return m.installerRunVersion(binaryVersion(m.binary))
}

func (m *world) labServesCutover() error {
	if _, err := m.runWith("roca migrate --json", []string{"migrate", "--json"}); err != nil {
		return err
	}
	if m.last.code != 0 {
		return fmt.Errorf("roca migrate exited %d: %s", m.last.code, m.last.stderr)
	}
	if err := m.jsonKeyEqualTo("verified", "true"); err != nil {
		return err
	}
	return m.serveCutoverLayout()
}

// operatorCommand is the installed executable the command names, run with
// ~/.local/bin on PATH.
func (m *world) operatorCommand(command string) (*exec.Cmd, error) {
	arguments, err := split(command)
	if err != nil {
		return nil, err
	}
	return exec.Command(filepath.Join(theBinariesDirectory(m.home), arguments[0]), arguments[1:]...), nil
}

func (m *world) iRunOnTheOperatorPath(command string) error {
	cmd, err := m.operatorCommand(command)
	if err != nil {
		return err
	}
	m.residents.operatorPath = true
	defer func() { m.residents.operatorPath = false }()
	return m.record(command, cmd)
}

func (m *world) iRunTwoAtTheSameTime(command string) error {
	m.residents.operatorPath = true
	environment := m.environment()
	m.residents.operatorPath = false
	type outcome struct {
		run run
		err error
	}
	done := make(chan outcome, 2)
	for range 2 {
		cmd, err := m.operatorCommand(command)
		if err != nil {
			return err
		}
		cmd.Env = environment
		var out, failures strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &failures
		go func() {
			started := time.Now()
			err := runWithHangGuard(cmd, e2eHangGuard)
			result := run{command: command, stdout: out.String(), stderr: failures.String(), elapsed: time.Since(started)}
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				result.code, err = exit.ExitCode(), nil
			}
			done <- outcome{result, err}
		}()
	}
	m.residents.queries = nil
	for range 2 {
		got := <-done
		if got.err != nil {
			return fmt.Errorf("run %q: %w", command, got.err)
		}
		m.residents.queries = append(m.residents.queries, got.run)
		m.everything = append(m.everything, got.run)
		if m.observeDurations {
			reportMeasuredDuration(m.durationWriter(), command, got.run.elapsed, nil)
		}
	}
	return nil
}

func (m *world) bothQueriesHaveEngine(engine string) error {
	if len(m.residents.queries) != 2 {
		return fmt.Errorf("%d queries ran, want 2", len(m.residents.queries))
	}
	for _, query := range m.residents.queries {
		m.last = query
		if err := m.itExitsWithCode(0); err != nil {
			return err
		}
		if err := m.queryEnginesInclude(engine); err != nil {
			return err
		}
	}
	return nil
}

func (m *world) exactlyOneVectorResident() error {
	ps, err := testfixture.ResidentPSHint(m.residents.socket)
	if err != nil {
		return err
	}
	if count := testfixture.CountResidentLines(ps); count != 1 {
		return fmt.Errorf("vector residents for the home = %d, want 1\n%s", count, ps)
	}
	return nil
}

// harborLanternExchangeAmong reads a vector answer and finds the corpus hit of
// the frozen session whose exchange mentions the harbor lantern.
func harborLanternExchangeAmong(raw string) error {
	var answer struct {
		Results []struct {
			Database string `json:"database"`
			Text     string `json:"text"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(raw), &answer); err != nil {
		return fmt.Errorf("the vector answer is not JSON: %w\n%s", err, raw)
	}
	for _, hit := range answer.Results {
		if hit.Database == "corpus" && strings.Contains(strings.ToLower(hit.Text), "harbor lantern") {
			return nil
		}
	}
	return fmt.Errorf("no corpus hit mentions the harbor lantern:\n%s", raw)
}
