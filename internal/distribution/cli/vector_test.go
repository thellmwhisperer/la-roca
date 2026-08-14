package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/vector"
)

func TestVectorInstallLaunchesOneBackgroundWorker(t *testing.T) {
	oldLaunch, oldExecutable := launchVectorWorker, vectorExecutable
	t.Cleanup(func() { launchVectorWorker, vectorExecutable = oldLaunch, oldExecutable })
	var request vector.LaunchRequest
	launchVectorWorker = func(got vector.LaunchRequest) (vector.LaunchResult, error) {
		request = got
		return vector.LaunchResult{PID: 42, LogPath: filepath.Join(got.DataDir, vector.WorkerLogFilename)}, nil
	}
	vectorExecutable = func() (string, error) { return "/synthetic/roca", nil }

	directory := t.TempDir()
	corePath := filepath.Join(directory, "roca.db")
	if err := os.WriteFile(corePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	env := &cliEnv{out: out, errOut: &bytes.Buffer{}, dbPath: corePath, skipReconciliation: true}
	code, err := executeWithEnv(env, []string{"--db-path", corePath, "vector", "install"}, strings.NewReader(""))
	if err != nil || code != 0 {
		t.Fatalf("install: code=%d err=%v", code, err)
	}
	if request.Executable != "/synthetic/roca" || request.DataDir != filepath.Join(directory, "vector") {
		t.Fatalf("launch request = %+v", request)
	}
	wantArgs := strings.Join(vector.WorkerArguments(corePath, vector.DefaultModel), "\x00")
	if got := strings.Join(request.Arguments, "\x00"); got != wantArgs {
		t.Fatalf("worker args = %q, want %q", got, wantArgs)
	}
	if !strings.Contains(out.String(), "background") || !strings.Contains(out.String(), "42") {
		t.Fatalf("install output = %q", out.String())
	}
}

func TestVectorDeltaFlagIsExplicit(t *testing.T) {
	env := &cliEnv{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, dbPath: filepath.Join(t.TempDir(), "roca.db"), skipReconciliation: true}
	code, err := executeWithEnv(env, []string{"--db-path", env.dbPath, "vector", "ingest"}, strings.NewReader(""))
	if err == nil || code == 0 || !strings.Contains(err.Error(), "--delta") {
		t.Fatalf("ingest without delta: code=%d err=%v", code, err)
	}
}

func TestVectorIsAnEnglishPublicCommand(t *testing.T) {
	if !publicCommand("vector") {
		t.Fatal("vector command is hidden")
	}
	env := &cliEnv{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	command, _, err := rootCommand(env).Find([]string{"vector"})
	if err != nil {
		t.Fatal(err)
	}
	if command.Name() != "vector" || strings.Contains(command.Short, "semánt") {
		t.Fatalf("vector command = %q %q", command.Name(), command.Short)
	}
}
