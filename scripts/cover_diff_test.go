package scripts_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func command(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func coverFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/fixture\n\ngo 1.20\n")
	command(t, dir, nil, "git", "init", "-q")
	command(t, dir, nil, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base")
	return dir, filepath.Join(root, "scripts", "cover-diff.sh"), strings.TrimSpace(command(t, dir, nil, "git", "rev-parse", "HEAD"))
}

func checkDiff(t *testing.T, dir, script, base, coverage string) (string, int) {
	t.Helper()
	cmd := exec.Command("sh", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "COVER_BASE="+base, "GOCOVERDIR="+coverage)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return string(out), exit.ExitCode()
	}
	t.Fatal(err)
	return "", -1
}

func TestCoverDiffNamesTheUncoveredChangedLines(t *testing.T) {
	dir, script, _ := coverFixture(t)
	source := `package main
import "os"
// base outside every block
func main() {
	if os.Args[1] == "a" {
		println("a base")
	}
	if os.Args[1] == "b" {
		println("b base")
		// base inside the blind block
	}
}
`
	ignored := []string{"main_test.go", "test/helper.go", "testdata/helper.go", "nested/testdata/helper.go", "plugins/helper.go"}
	write(t, filepath.Join(dir, "main.go"), source)
	for _, path := range ignored {
		write(t, filepath.Join(dir, path), "package main\n// base\n")
	}
	command(t, dir, nil, "git", "add", ".")
	command(t, dir, nil, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	base := strings.TrimSpace(command(t, dir, nil, "git", "rev-parse", "HEAD"))
	write(t, filepath.Join(dir, "main.go"), strings.ReplaceAll(source, "base", "changed"))
	for _, path := range ignored {
		write(t, filepath.Join(dir, path), "package main\n// changed\n")
	}
	binary, coverage := filepath.Join(dir, "fixture"), t.TempDir()
	command(t, dir, nil, "go", "build", "-cover", "-o", binary, ".")
	env := append(os.Environ(), "GOCOVERDIR="+coverage)
	command(t, dir, env, binary, "a")
	before := command(t, dir, nil, "git", "status", "--porcelain", "--untracked-files=all")
	want := "cover-diff: these changed lines are not executed by any end-to-end run of the real roca binary. Delete them, or write the acceptance scenario that reaches them from a real roca command.\nmain.go:9\nmain.go:10\n"
	for range 2 {
		out, code := checkDiff(t, dir, script, base, coverage)
		if code != 1 || out != want {
			t.Fatalf("exit %d, output:\n%s\nwant exit 1 and:\n%s", code, out, want)
		}
	}
	if after := command(t, dir, nil, "git", "status", "--porcelain", "--untracked-files=all"); after != before {
		t.Fatalf("checker changed its tree:\nbefore: %s\nafter: %s", before, after)
	}
	command(t, dir, env, binary, "b")
	if out, code := checkDiff(t, dir, script, base, coverage); code != 0 || out != "cover-diff: 3 changed lines inside covered blocks, all executed by an end-to-end run\n" {
		t.Fatalf("after both branches: exit %d\n%s", code, out)
	}
	command(t, dir, nil, "git", "update-ref", "refs/remotes/origin/integration", base)
	if out, code := checkDiff(t, dir, script, "", coverage); code != 0 {
		t.Fatalf("default integration base: exit %d\n%s", code, out)
	}
}

func TestCoverDiffNoGoChangesAndMissingBase(t *testing.T) {
	dir, script, base := coverFixture(t)
	write(t, filepath.Join(dir, "notes.md"), "not Go\n")
	if out, code := checkDiff(t, dir, script, base, ""); code != 0 || out != "cover-diff: no Go changes\n" {
		t.Fatalf("no Go changes: exit %d\n%s", code, out)
	}
	branches := command(t, dir, nil, "git", "branch", "-a")
	if out, code := checkDiff(t, dir, script, "", ""); code != 1 || !strings.Contains(out, "cover-diff: cannot find the base commit\n"+branches) {
		t.Fatalf("missing base: exit %d\n%s", code, out)
	}
}

func TestMakeBuildInstrumentsOnlyUnderGoCoverDir(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	binary := filepath.Join(temp, "bin", "roca")
	build := func(coverage string, extra ...string) []byte {
		t.Helper()
		// The existing CGO-free vector build keeps this core check independent of native model dependencies.
		args := []string{"build", "HOST_OS=windows", "BIN=" + binary, "VECTOR_TMP=" + temp, "VERSION=cover-test", "COMMIT=test", "DATE=2000-01-01T00:00:00Z"}
		env := []string{}
		for _, item := range os.Environ() {
			if !strings.HasPrefix(item, "GOCOVERDIR=") {
				env = append(env, item)
			}
		}
		if coverage != "" {
			env = append(env, "GOCOVERDIR="+coverage)
		}
		command(t, root, env, "make", append(args, extra...)...)
		raw, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	assertCounters := func(coverage string, want bool) {
		t.Helper()
		command(t, root, append(os.Environ(), "GOCOVERDIR="+coverage), binary, "version")
		files, err := filepath.Glob(filepath.Join(coverage, "covcounters.*"))
		if err != nil || (len(files) > 0) != want {
			t.Fatalf("version wrote %d counter files, want counters=%v: %v", len(files), want, err)
		}
	}
	coverage := t.TempDir()
	build(coverage)
	assertCounters(coverage, true)
	plain := build("")
	assertCounters(t.TempDir(), false)
	previous := build("", fmt.Sprintf("GO_BUILD=CGO_ENABLED=0 go build -trimpath -ldflags %q", "-s -w -X main.version=cover-test -X main.commit=test -X main.date=2000-01-01T00:00:00Z"))
	if !bytes.Equal(plain, previous) {
		t.Fatal("uninstrumented make build differs from the previous build command")
	}
}

func TestCoverDiffMakeTargetStopsAtFirstFailure(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, codes := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		t.Run(fmt.Sprint(codes), func(t *testing.T) {
			dir := t.TempDir()
			accept := filepath.Join(dir, "accept.sh")
			checker := filepath.Join(dir, "scripts", "cover-diff.sh")
			write(t, accept, `#!/bin/sh
set -eu
test -d "$GOCOVERDIR"
for file in "$GOCOVERDIR"/*; do
    if [ -e "$file" ]; then exit 9; fi
done
printf '%s\n' "$GOCOVERDIR" > accept-called
exit "$ACCEPT_CODE"
`)
			write(t, checker, `#!/bin/sh
set -eu
read -r directory < accept-called
test "$directory" = "$GOCOVERDIR"
printf 'checked\n' > check-called
exit "$CHECK_CODE"
`)
			for _, script := range []string{accept, checker} {
				if err := os.Chmod(script, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("make", "-f", filepath.Join(root, "Makefile"), "e2e-cover", "MAKE="+accept)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), fmt.Sprintf("ACCEPT_CODE=%d", codes[0]), fmt.Sprintf("CHECK_CODE=%d", codes[1]))
			out, err := cmd.CombinedOutput()
			if (err == nil) != (codes == [2]int{0, 0}) {
				t.Fatalf("accept/check exits %v: %v\n%s", codes, err, out)
			}
			if _, err := os.Stat(filepath.Join(dir, "accept-called")); err != nil {
				t.Fatalf("accept did not run in an empty coverage directory: %v\n%s", err, out)
			}
			if _, err := os.Stat(filepath.Join(dir, "check-called")); (err == nil) != (codes[0] == 0) {
				t.Fatalf("checker must run only after acceptance succeeds: %v\n%s", err, out)
			}
		})
	}
}
