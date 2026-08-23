package vector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type captureNotifier struct{ notification Completion }

func (n *captureNotifier) Notify(_ context.Context, completion Completion) error {
	n.notification = completion
	return nil
}

func TestWorkerRecordsAndNotifiesSuccessfulCompletion(t *testing.T) {
	corpus := createCoreFixture(t)
	directory := t.TempDir()
	notifier := &captureNotifier{}
	worker := Worker{
		Index:   Index{Corpus: corpus, VectorPath: filepath.Join(directory, "vector.db"), Model: DefaultModel, Embedder: &recordingEmbedder{}},
		DataDir: directory, PullModel: true, Notifier: notifier,
		WaitForCalm: func(context.Context) error { return nil },
	}
	completion := worker.Run(context.Background())
	if completion.ExitStatus != 0 || completion.Delta.Added != 8 {
		t.Fatalf("completion = %+v", completion)
	}
	if notifier.notification.ExitStatus != 0 || notifier.notification.Delta.Added != 8 {
		t.Fatalf("notification = %+v", notifier.notification)
	}
	if _, err := os.Stat(filepath.Join(directory, CompletionFilename)); err != nil {
		t.Fatalf("completion record: %v", err)
	}
}

func TestLaunchReportsTheClaimedWorkerPID(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell to launch: %v", err)
	}
	directory := t.TempDir()
	result, err := Launch(LaunchRequest{Executable: shell, Arguments: []string{"-c", "exit 0"}, DataDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	if claimed := ReadWorkerPID(directory); result.PID <= 0 || result.PID != claimed {
		t.Fatalf("reported pid %d, claim file pid %d", result.PID, claimed)
	}
}

func TestLaunchKeepsLiveProgressConnectedAfterLauncherReturns(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell to launch: %v", err)
	}
	directory := t.TempDir()
	progressPath := filepath.Join(directory, "progress")
	progress, err := os.OpenFile(progressPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer progress.Close()
	_, err = Launch(LaunchRequest{Executable: shell,
		Arguments: []string{"-c", "sleep 0.1; echo 'semantic index: 1/2 chunks' >&3"},
		DataDir:   directory, Progress: progress})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		body, _ := os.ReadFile(progressPath)
		if strings.Contains(string(body), "semantic index: 1/2 chunks") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("detached worker did not retain the live progress channel")
}

func TestWorkerClaimDistinguishesLiveAndStaleProcesses(t *testing.T) {
	directory := t.TempDir()
	claimPath := filepath.Join(directory, WorkerClaimFilename)
	if err := os.WriteFile(claimPath, []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := workerProcessAlive
	t.Cleanup(func() { workerProcessAlive = old })
	workerProcessAlive = func(int) bool { return true }
	claim, err := claimWorker(claimPath)
	if err != nil || claim != nil {
		t.Fatalf("live claim = %v, err=%v", claim, err)
	}

	workerProcessAlive = func(int) bool { return false }
	claim, err = claimWorker(claimPath)
	if err != nil || claim == nil {
		t.Fatalf("stale claim = %v, err=%v", claim, err)
	}
	claim.Close()
}

func TestManagedStateUsageLocksTheStablePluginRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	state := filepath.Join(root, "vector", "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := LockStateUsage(state)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := os.Stat(filepath.Join(root, relocationLockFile)); err != nil {
		t.Fatalf("stable relocation lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, ".relocation.lock")); !os.IsNotExist(err) {
		t.Fatalf("state-local relocation lock = %v", err)
	}
}
