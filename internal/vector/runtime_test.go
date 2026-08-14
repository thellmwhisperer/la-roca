package vector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type captureNotifier struct{ notification Completion }

func (n *captureNotifier) Notify(_ context.Context, completion Completion) error {
	n.notification = completion
	return nil
}

func TestWorkerRecordsAndNotifiesSuccessfulCompletion(t *testing.T) {
	corePath := createCoreFixture(t)
	directory := t.TempDir()
	notifier := &captureNotifier{}
	worker := Worker{
		Index:   Index{CorePath: corePath, VectorPath: filepath.Join(directory, "vector.db"), Model: DefaultModel, Embedder: &recordingEmbedder{}},
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
