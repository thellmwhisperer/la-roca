//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type distributionRun struct {
	code   int
	stdout string
	stderr string
}

type distributionWorld struct {
	binary    string
	root      string
	home      string
	installed string
	last      distributionRun
	human     distributionRun
	machine   distributionRun
	session   *mcp.ClientSession
	tool      *mcp.CallToolResult
	tools     *mcp.ListToolsResult
	state     map[string]any
}

func registerDistributionSteps(ctx *godog.ScenarioContext, binary string) {
	w := &distributionWorld{binary: binary}
	ctx.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
		repo, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			return c, err
		}
		tmp := filepath.Join(repo, ".tmp")
		if err := os.MkdirAll(tmp, 0o700); err != nil {
			return c, err
		}
		w.root, err = os.MkdirTemp(tmp, "distribution-acceptance-")
		w.home, w.installed = "", ""
		w.last, w.human, w.machine = distributionRun{}, distributionRun{}, distributionRun{}
		w.session, w.tool, w.tools = nil, nil, nil
		w.state = map[string]any{}
		return c, err
	})
	ctx.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		w.closeMCP()
		if w.root != "" {
			_ = os.RemoveAll(w.root)
		}
		return c, nil
	})

	ctx.Given(`^an isolated La Roca distribution$`, func() error { return nil })
	registerDistributionCLISteps(ctx, w)
	registerDistributionMCPSteps(ctx, w)
	registerDistributionSkillSteps(ctx, w)
	registerDistributionLifecycleSteps(ctx, w)
}

func (w *distributionWorld) prepare(label string) error {
	home := filepath.Join(w.root, label)
	if err := os.MkdirAll(filepath.Join(home, ".tmp"), 0o700); err != nil {
		return err
	}
	installed := filepath.Join(home, "roca")
	if err := copyAcceptanceFile(w.binary, installed, 0o755); err != nil {
		return err
	}
	run := w.runAt(home, installed, "init", "--db-path", filepath.Join(home, ".roca", "roca.db"), "--json")
	if run.code != 0 {
		return fmt.Errorf("initialize %s: exit %d: %s", label, run.code, run.stderr)
	}
	w.home, w.installed, w.last = home, installed, run
	return nil
}

func (w *distributionWorld) ensurePrepared() error {
	if w.installed != "" {
		return nil
	}
	return w.prepare("default")
}

func (w *distributionWorld) run(args ...string) distributionRun {
	if err := w.ensurePrepared(); err != nil {
		return distributionRun{code: -1, stderr: err.Error()}
	}
	w.last = w.runAt(w.home, w.installed, args...)
	return w.last
}

func (w *distributionWorld) runAt(home, binary string, args ...string) distributionRun {
	return w.runAtInput(home, binary, "", nil, args...)
}

func (w *distributionWorld) runAtInput(home, binary, input string, extraEnv []string, args ...string) distributionRun {
	cmd := exec.Command(binary, args...)
	cmd.Env = distributionEnvironment(home, binary)
	for _, assignment := range extraEnv {
		key, _, _ := strings.Cut(assignment, "=")
		cmd.Env = replaceEnvironment(cmd.Env, key, assignment)
	}
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			code = -1
			stderr.WriteString(err.Error())
		}
	}
	return distributionRun{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func replaceEnvironment(env []string, key, assignment string) []string {
	prefix := key + "="
	filtered := env[:0]
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			filtered = append(filtered, item)
		}
	}
	return append(filtered, assignment)
}

func distributionEnvironment(home, binary string) []string {
	blocked := map[string]bool{
		"HOME": true, "TMPDIR": true, "ROCA_BIN": true,
		"ROCA_DB_PATH": true, "ROCA_CONFIG": true, "ROCA_MODELS_ORDER": true,
		"CLAUDE_CONFIG_DIR": true, "CODEX_HOME": true, "OPENCODE_CONFIG": true,
		"HERMES_HOME": true, "PI_CODING_AGENT_DIR": true,
	}
	env := make([]string, 0, len(os.Environ())+4)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !blocked[key] {
			env = append(env, item)
		}
	}
	env = append(env,
		"HOME="+home,
		"TMPDIR="+filepath.Join(home, ".tmp"),
		"ROCA_BIN="+binary,
	)
	if _, err := os.Stat(filepath.Join(home, ".roca", "config.toml")); os.IsNotExist(err) {
		env = append(env, "ROCA_MODELS_ORDER=none")
	}
	certificate := filepath.Join(home, "tls-ca.pem")
	if _, err := os.Stat(certificate); err == nil {
		env = append(env, "SSL_CERT_FILE="+certificate)
	}
	return env
}

func copyAcceptanceFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (w *distributionWorld) openMCP() error {
	if w.session != nil {
		return nil
	}
	if err := w.ensurePrepared(); err != nil {
		return err
	}
	command := exec.Command(w.installed, "mcp", "serve")
	command.Env = distributionEnvironment(w.home, w.installed)
	var stderr strings.Builder
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "distribution-acceptance", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		return fmt.Errorf("open MCP stdio session: %w (%s)", err, stderr.String())
	}
	w.session = session
	return nil
}

func (w *distributionWorld) callTool(name string, arguments map[string]any) error {
	if err := w.openMCP(); err != nil {
		return err
	}
	result, err := w.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return err
	}
	if result.IsError {
		return fmt.Errorf("%s returned an error: %s", name, renderedText(result))
	}
	w.tool = result
	return nil
}

func (w *distributionWorld) closeMCP() {
	if w.session != nil {
		_ = w.session.Close()
	}
	w.session = nil
}
