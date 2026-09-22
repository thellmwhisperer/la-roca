package securefile

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsPublicationOperations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := CreatePreservingParentMode(path, []byte("created"), 0o600, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Replace(path, []byte("replaced"), []byte("created")); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("written"), 0o600, 0o700); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "written" {
		t.Fatalf("published content = %q, error = %v", content, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatalf("publication left unexpected entries: %v, error = %v", entries, err)
	}
}

func TestWindowsDirectoryMutexAcrossProcesses(t *testing.T) {
	for _, kill := range []bool{false, true} {
		t.Run(fmt.Sprintf("kill=%t", kill), func(t *testing.T) {
			path, previous := secureFileFixture(t, "config.json", "operator")
			dir := filepath.Dir(path)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsDirectoryMutexHelper$")
			cmd.Env = append(os.Environ(), "ROCA_TEST_DIRECTORY_MUTEX="+dir)
			cmd.Stderr = os.Stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = cmd.Process.Kill()
				if cmd.ProcessState == nil {
					_ = cmd.Wait()
				}
			}()
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "locked" {
				t.Fatalf("child did not acquire directory lock: %q, %v", scanner.Text(), scanner.Err())
			}
			mutex, err := openDirectoryMutex(dir + string(os.PathSeparator) + ".")
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(mutex)
			runtime.LockOSThread()
			status, waitErr := windows.WaitForSingleObject(mutex, 0)
			if status == windows.WAIT_OBJECT_0 || status == windows.WAIT_ABANDONED {
				_ = windows.ReleaseMutex(mutex)
			}
			runtime.UnlockOSThread()
			if waitErr != nil || status != uint32(windows.WAIT_TIMEOUT) {
				t.Fatalf("competing lock status = %d, error = %v, want timeout", status, waitErr)
			}
			if kill {
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := stdin.Write([]byte("release\n")); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); !kill && err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(path)
			if err != nil || string(content) != string(previous) {
				t.Fatalf("operator content = %q, error = %v", content, err)
			}
			if err := Replace(path, []byte("after release"), previous); err != nil {
				t.Fatalf("publication after child exit: %v", err)
			}
			content, err = os.ReadFile(path)
			if err != nil || string(content) != "after release" {
				t.Fatalf("published content = %q, error = %v", content, err)
			}
		})
	}
}

func TestWindowsDirectoryMutexHelper(t *testing.T) {
	dir := os.Getenv("ROCA_TEST_DIRECTORY_MUTEX")
	if dir == "" {
		return
	}
	release, err := lockDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("locked")
	bufio.NewScanner(os.Stdin).Scan()
	if err := release(); err != nil {
		t.Fatal(err)
	}
}
