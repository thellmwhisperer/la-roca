package rocavector

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkerRunningRequiresTheClaimedProcessStart(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, Name, StateDir)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	processIdentity, err := processStartIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(state, workerClaimFilename)
	if err := os.WriteFile(claimPath,
		[]byte(fmt.Sprintf("%d current-run %s\n", os.Getpid(), processIdentity)), 0o600); err != nil {
		t.Fatal(err)
	}
	if !WorkerRunning(root) {
		t.Fatal("matching worker process identity was reported stopped")
	}
	if err := os.WriteFile(claimPath,
		[]byte(fmt.Sprintf("%d previous-run 1\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if WorkerRunning(root) {
		t.Fatal("reused pid was reported as the vector worker")
	}
}

func TestReserveWorkerReclaimsAClaimAfterPIDReuse(t *testing.T) {
	state := t.TempDir()
	claimPath := filepath.Join(state, workerClaimFilename)
	if err := os.WriteFile(claimPath,
		[]byte(fmt.Sprintf("%d previous-run 1\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	reservation, err := reserveWorker(state)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.info == nil {
		t.Fatal("replacement reservation was not created")
	}
}
