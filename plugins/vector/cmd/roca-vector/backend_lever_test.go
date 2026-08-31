package main

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/thellmwhisperer/la-roca-vector/internal/llamacpp"
	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
)

// install is a foreground bulk job the operator is waiting on, and the lever it
// was given has to survive the hop into the detached worker process.
func TestInstallForwardsTheAcceleratorLeverToTheWorker(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  string
		want string
	}{
		{name: "no lever leaves the worker on its own default", args: []string{"install"}},
		{name: "accelerate flag beats cpu environment", args: []string{"install", "--accelerate"},
			env: "0", want: "ROCA_VECTOR_WRITER_GPU=1"},
		{name: "forced cpu flag beats accelerator environment", args: []string{"install", "--accelerate=false"},
			env: "1", want: "ROCA_VECTOR_WRITER_GPU=0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ROCA_VECTOR_PLUGIN_ROOT", "")
			t.Setenv("ROCA_VECTOR_WRITER_GPU", test.env)
			oldLaunch, oldExecutable := launchWorker, currentExecutable
			t.Cleanup(func() { launchWorker, currentExecutable = oldLaunch, oldExecutable })
			var request vector.LaunchRequest
			launchWorker = func(got vector.LaunchRequest) (vector.LaunchResult, error) {
				request = got
				return vector.LaunchResult{PID: 42,
					LogPath: filepath.Join(got.DataDir, vector.WorkerLogFilename)}, nil
			}
			currentExecutable = func() (string, error) { return "/synthetic/roca-vector", nil }
			t.Setenv("ROCA_VECTOR_STATE_DIR", filepath.Join(t.TempDir(), "state"))
			root := rootCommand(&environment{dbPath: "/synthetic/roca.db"})
			root.SetArgs(test.args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			carried := ""
			for _, entry := range request.Environment {
				if len(entry) > len("ROCA_VECTOR_WRITER_GPU=") &&
					entry[:len("ROCA_VECTOR_WRITER_GPU=")] == "ROCA_VECTOR_WRITER_GPU=" {
					carried = entry
				}
			}
			if test.want == "" {
				if carried != "" {
					t.Fatalf("worker environment carried %q with no lever set", carried)
				}
				return
			}
			if !slices.Contains(request.Environment, test.want) {
				t.Fatalf("worker environment = %q, want %q", request.Environment, test.want)
			}
		})
	}
}

// The occasion is decided by the command that runs: a background delta stays
// conservative, a reembed rebuild is a bulk job, and the worker builds in bulk.
func TestWriterPolicyForCommandPicksTheOccasion(t *testing.T) {
	forced := func(value bool) *bool { return &value }
	tests := []struct {
		name     string
		reembed  bool
		flag     *bool
		env      string
		occasion llamacpp.Occasion
		lever    llamacpp.Lever
	}{
		{name: "delta default", occasion: llamacpp.OccasionDelta},
		{name: "reembed is bulk", reembed: true, occasion: llamacpp.OccasionBulk},
		{name: "env lever reaches a delta", env: "1",
			occasion: llamacpp.OccasionDelta, lever: llamacpp.LeverAccelerate},
		{name: "flag beats env", flag: forced(false), env: "1",
			occasion: llamacpp.OccasionDelta, lever: llamacpp.LeverCPU},
		{name: "flag forces cpu on a bulk build", reembed: true, flag: forced(false),
			occasion: llamacpp.OccasionBulk, lever: llamacpp.LeverCPU},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ROCA_VECTOR_WRITER_GPU", test.env)
			got := writerPolicy(llamacpp.WriterOccasion(test.reembed), test.flag)
			if got.Occasion != test.occasion || got.Lever != test.lever {
				t.Fatalf("policy = %+v, want occasion %q lever %q",
					got, test.occasion, test.lever)
			}
		})
	}
}

// The worker process is the install build: bulk by default, and it reads the
// lever the parent handed down.
func TestWorkerDefaultsToABulkBuild(t *testing.T) {
	t.Setenv("ROCA_VECTOR_WRITER_GPU", "")
	if got := writerPolicy(llamacpp.OccasionBulk, nil); got.Occasion != llamacpp.OccasionBulk ||
		got.Lever != llamacpp.LeverUnset {
		t.Fatalf("worker policy = %+v, want a bulk build with no lever", got)
	}
	t.Setenv("ROCA_VECTOR_WRITER_GPU", "0")
	if got := writerPolicy(llamacpp.OccasionBulk, nil); got.Lever != llamacpp.LeverCPU {
		t.Fatalf("worker policy = %+v, want the inherited cpu lever", got)
	}
}
