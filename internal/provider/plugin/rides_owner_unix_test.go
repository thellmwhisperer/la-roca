//go:build !windows

package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOperatorRideFileRejectsWrongOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rides.toml")
	if err := os.WriteFile(path, []byte(`[ride.backup]
command = "echo backup"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("file owner is unavailable")
	}

	if err := operatorRideFileAllowed(path, nil, info, int(stat.Uid)+1); err == nil ||
		!strings.Contains(err.Error(), "not owned by the user running roca cron") {
		t.Fatalf("wrong owner: %v", err)
	}
}

func TestOperatorRideSymlinkUsesTrustedTarget(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "rides.toml")
	link := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(target, []byte(`[ride.backup]
command = "echo backup"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	rides, warnings, err := DiscoverOperatorRides(link, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rides) != 1 || len(warnings) != 0 || rides[0].Name != "backup" {
		t.Fatalf("trusted symlink discovery = %+v warnings = %v", rides, warnings)
	}
}

func TestOperatorRideFIFOIsRefusedWithoutBlocking(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "rides.toml")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, _, err := DiscoverOperatorRides("", directory)
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("FIFO discovery error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO discovery blocked")
	}
}
