package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/vector"
)

// syntheticCoreDatabase is the file the vector commands only stat: an empty
// core database is enough for everything they decide before touching SQLite.
func syntheticCoreDatabase(t *testing.T) string {
	t.Helper()
	corePath := filepath.Join(t.TempDir(), "roca.db")
	if err := os.WriteFile(corePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return corePath
}

func TestVectorInstallLaunchesOneBackgroundWorker(t *testing.T) {
	oldLaunch, oldExecutable := launchVectorWorker, vectorExecutable
	t.Cleanup(func() { launchVectorWorker, vectorExecutable = oldLaunch, oldExecutable })
	var request vector.LaunchRequest
	launchVectorWorker = func(got vector.LaunchRequest) (vector.LaunchResult, error) {
		request = got
		return vector.LaunchResult{PID: 42, LogPath: filepath.Join(got.DataDir, vector.WorkerLogFilename)}, nil
	}
	vectorExecutable = func() (string, error) { return "/synthetic/roca", nil }

	corePath := syntheticCoreDatabase(t)
	out := &bytes.Buffer{}
	env := &cliEnv{out: out, errOut: &bytes.Buffer{}, dbPath: corePath, skipReconciliation: true}
	code, err := executeWithEnv(env, []string{"--db-path", corePath, "vector", "install"}, strings.NewReader(""))
	if err != nil || code != 0 {
		t.Fatalf("install: code=%d err=%v", code, err)
	}
	if request.Executable != "/synthetic/roca" || request.DataDir != filepath.Join(filepath.Dir(corePath), "vector") {
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

func TestVectorWritesRefuseReadOnlyMode(t *testing.T) {
	oldLaunch := launchVectorWorker
	t.Cleanup(func() { launchVectorWorker = oldLaunch })
	launched := false
	launchVectorWorker = func(vector.LaunchRequest) (vector.LaunchResult, error) {
		launched = true
		return vector.LaunchResult{}, nil
	}
	corePath := syntheticCoreDatabase(t)
	t.Setenv(config.EnvReadOnly, "1")

	for _, testCase := range []struct {
		name      string
		arguments []string
		operation string
	}{
		{name: "install", arguments: []string{"vector", "install"}, operation: "vector install"},
		{name: "delta", arguments: []string{"vector", "ingest", "--delta"}, operation: "vector ingest --delta"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			env := &cliEnv{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, dbPath: corePath, skipReconciliation: true}
			code, err := executeWithEnv(env, append([]string{"--db-path", corePath}, testCase.arguments...), strings.NewReader(""))
			if err == nil || code == 0 {
				t.Fatalf("%s under read-only: code=%d err=%v", testCase.name, code, err)
			}
			if !strings.Contains(err.Error(), "read-only mode") || !strings.Contains(err.Error(), testCase.operation) {
				t.Fatalf("%s error = %v", testCase.name, err)
			}
		})
	}
	if launched {
		t.Fatal("read-only install launched a background worker")
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
