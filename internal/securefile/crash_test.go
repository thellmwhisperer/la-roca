package securefile

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPublicationCrashPreservesOperatorPath(t *testing.T) {
	for _, stage := range []string{"before-refusal", "after-refusal", "before-create", "after-create"} {
		for _, edit := range []string{"in-place", "rename"} {
			t.Run(stage+"/"+edit, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.json")
				if stage == "before-refusal" || stage == "after-refusal" {
					if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := publicationChild(ctx, path, stage)
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				stdin, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				defer stdin.Close()
				cmd.Stderr = os.Stderr
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
				if !scanner.Scan() || scanner.Text() != "paused" {
					t.Fatalf("child did not reach %s: %q, %v", stage, scanner.Text(), scanner.Err())
				}
				if stage == "before-create" {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatalf("candidate visible before publication: %v", err)
					}
				} else {
					want := "old"
					if stage == "after-create" {
						want = "candidate"
					}
					assertFileContentAndMode(t, path, []byte(want), 0o600)
				}
				operator := []byte("operator saved during " + stage)
				destination := path
				if edit == "rename" {
					destination = filepath.Join(filepath.Dir(path), "editor-save")
				}
				if err := os.WriteFile(destination, operator, 0o600); err != nil {
					t.Fatal(err)
				}
				if edit == "rename" {
					if err := os.Rename(destination, path); err != nil {
						t.Fatal(err)
					}
				}
				assertFileContentAndMode(t, path, operator, 0o600)
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				if err := cmd.Wait(); err == nil {
					t.Fatal("child exited successfully instead of being killed")
				}
				assertFileContentAndMode(t, path, operator, 0o600)
				beforeRestart := publicationFiles(t, filepath.Dir(path))
				for name, data := range beforeRestart {
					if name != "config.json" && (stage != "before-create" || data != "candidate") {
						t.Fatalf("unexpected displaced content in %s: %q", name, data)
					}
				}
				restart := publicationChild(ctx, path, "restart")
				if output, err := restart.CombinedOutput(); err != nil {
					t.Fatalf("restart: %v\n%s", err, output)
				}
				assertFileContentAndMode(t, path, operator, 0o600)
				if after := publicationFiles(t, filepath.Dir(path)); !reflect.DeepEqual(beforeRestart, after) {
					t.Fatalf("restart changed persisted files: before=%v after=%v", beforeRestart, after)
				}
			})
		}
	}
}

func publicationChild(ctx context.Context, path, stage string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPublicationCrashHelper$")
	cmd.Env = append(os.Environ(), "ROCA_TEST_PUBLICATION_PATH="+path, "ROCA_TEST_PUBLICATION_STAGE="+stage)
	return cmd
}

func publicationFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string]string)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name()] = string(data)
	}
	return files
}

func TestPublicationCrashHelper(t *testing.T) {
	path := os.Getenv("ROCA_TEST_PUBLICATION_PATH")
	if path == "" {
		return
	}
	stage := os.Getenv("ROCA_TEST_PUBLICATION_STAGE")
	pause := func() {
		fmt.Println("paused")
		bufio.NewScanner(os.Stdin).Scan()
		t.Fatal("parent must kill the paused child")
	}
	switch stage {
	case "before-refusal", "after-refusal", "restart":
		previous, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if stage == "before-refusal" {
			beforePublication = func(string) { pause() }
		}
		result, err := ReplaceWithResult(path, []byte("candidate"), previous)
		if !errors.Is(err, ErrConditionalReplaceUnsupported) || result.Identity.Valid() {
			t.Fatalf("replacement = %+v, %v, want unsupported refusal", result, err)
		}
		if stage == "after-refusal" {
			pause()
		}
	case "before-create", "after-create":
		realRename := renameNoReplaceFile
		renameNoReplaceFile = func(staged, target string) error {
			if stage == "before-create" {
				pause()
			}
			err := realRename(staged, target)
			if err == nil && stage == "after-create" {
				pause()
			}
			return err
		}
		if err := Replace(path, []byte("candidate"), nil); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown child stage %q", stage)
	}
}
