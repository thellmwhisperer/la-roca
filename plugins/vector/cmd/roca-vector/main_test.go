package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
)

func TestInstallLaunchesThePluginBinaryIntoManifestOwnedState(t *testing.T) {
	oldLaunch, oldExecutable := launchWorker, currentExecutable
	t.Cleanup(func() { launchWorker, currentExecutable = oldLaunch, oldExecutable })
	var request vector.LaunchRequest
	launchWorker = func(got vector.LaunchRequest) (vector.LaunchResult, error) {
		request = got
		return vector.LaunchResult{PID: 42, LogPath: filepath.Join(got.DataDir, vector.WorkerLogFilename)}, nil
	}
	currentExecutable = func() (string, error) { return "/synthetic/roca-vector", nil }
	state := filepath.Join(t.TempDir(), "state")
	env := &environment{dbPath: "/synthetic/roca.db", stateDir: state}
	root := rootCommand(env)
	root.SetArgs([]string{"install"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	want := workerArguments(env.dbPath, state, vector.DefaultModel)
	if request.Executable != "/synthetic/roca-vector" || request.DataDir != state ||
		!slices.Equal(request.Arguments, want) {
		t.Fatalf("launch request = %+v, want args %q", request, want)
	}
}

func TestDeltaFlagAndReadOnlyBoundaryAreExplicit(t *testing.T) {
	env := &environment{stateDir: t.TempDir()}
	root := rootCommand(env)
	root.SetArgs([]string{"ingest"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--delta") {
		t.Fatalf("ingest without delta = %v", err)
	}
	t.Setenv("ROCA_READ_ONLY", "1")
	root = rootCommand(env)
	root.SetArgs([]string{"install"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "ROCA_READ_ONLY") {
		t.Fatalf("install under read-only = %v", err)
	}
}

func TestWorkerCarriesExplicitCoreAndStatePaths(t *testing.T) {
	got := workerArguments("/synthetic/roca.db", "/synthetic/state", "synthetic-model")
	want := []string{"--state-dir", "/synthetic/state", "--db-path", "/synthetic/roca.db",
		"_worker", "--model", "synthetic-model"}
	if !slices.Equal(got, want) {
		t.Fatalf("worker arguments = %q, want %q", got, want)
	}
}

func TestVocabDemandsOneConceptAndAnInstalledIndex(t *testing.T) {
	env := &environment{stateDir: t.TempDir()}
	root := rootCommand(env)
	root.SetArgs([]string{"vocab"})
	if err := root.Execute(); err == nil {
		t.Fatal("vocab without a concept ran")
	}
	root = rootCommand(env)
	root.SetArgs([]string{"vocab", "salud"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "roca vector install") {
		t.Fatalf("vocab without an index = %v, want the install hint", err)
	}
}
