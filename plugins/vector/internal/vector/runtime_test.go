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
	result, err := Launch(LaunchRequest{Executable: shell, Arguments: []string{"-c", "sleep 0.1"}, DataDir: directory})
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
	if err := os.WriteFile(claimPath, []byte("123 current-run 99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldAlive, oldStart := workerProcessAlive, workerProcessStart
	t.Cleanup(func() {
		workerProcessAlive = oldAlive
		workerProcessStart = oldStart
	})
	workerProcessAlive = func(int) bool { return true }
	workerProcessStart = func(int) (int64, error) { return 99, nil }
	claim, err := claimWorker(claimPath)
	if err != nil || claim != nil {
		t.Fatalf("live claim = %v, err=%v", claim, err)
	}

	workerProcessStart = func(int) (int64, error) { return 100, nil }
	claim, err = claimWorker(claimPath)
	if err != nil || claim == nil {
		t.Fatalf("stale claim = %v, err=%v", claim, err)
	}
	claim.Close()
}

func TestWorkerRunningRequiresTheClaimOwnerLock(t *testing.T) {
	directory := t.TempDir()
	claimPath := filepath.Join(directory, WorkerClaimFilename)
	writeTestWorkerClaim(t, claimPath, "current-run")
	if WorkerRunning(directory) {
		t.Fatal("unlocked stale claim reported a running worker")
	}
	release, err := LockWorkerClaim(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !WorkerRunning(directory) {
		_ = release()
		t.Fatal("locked live claim did not report a running worker")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if WorkerRunning(directory) {
		t.Fatal("released claim owner lock still reported a running worker")
	}
}

func TestLockWorkerClaimRequiresActivityInvalidation(t *testing.T) {
	directory := t.TempDir()
	claimPath := filepath.Join(directory, WorkerClaimFilename)
	writeTestWorkerClaim(t, claimPath, "current-run")
	activityPath := filepath.Join(directory, workerActivityFile)
	if err := os.Mkdir(activityPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activityPath, "blocked"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := LockWorkerClaim(directory)
	if err == nil {
		if release != nil {
			_ = release()
		}
		t.Fatal("worker claim lock accepted uncleared activity")
	}
	if release != nil {
		_ = release()
		t.Fatal("failed activity invalidation retained the claim lock")
	}
	if !strings.Contains(err.Error(), "clear vector worker activity") {
		t.Fatalf("lock error = %v", err)
	}
	if WorkerRunning(directory) {
		t.Fatal("failed activity invalidation left the worker running")
	}
}

func writeTestWorkerClaim(t *testing.T, path, runID string) {
	t.Helper()
	raw, err := EncodeWorkerClaim(os.Getpid(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
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
