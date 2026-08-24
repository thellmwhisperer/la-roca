package mcpplug

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestPluginCompanionDiesWhenTheSessionClosesStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("session companions are proven on unix stdio")
	}
	root, data := t.TempDir(), t.TempDir()
	marker := filepath.Join(data, "dead")
	pid := filepath.Join(data, "pid")
	installCompanion(t, root, "mirror", "roca-mirror", []string{},
		"#!/bin/sh\necho $$ > "+pid+"\ntrap 'echo gone > "+marker+"' EXIT\nwhile IFS= read -r line; do :; done\n")
	notices := &safeBuffer{}
	set := startPluginCompanionsWithPolicy(root, data, notices, fastCompanionPolicy)
	waitFor(t, func() bool {
		_, err := os.Stat(pid)
		return err == nil
	})
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	if set.alive() {
		t.Fatal("companion outlived session close")
	}
}

func TestMissingCompanionLeavesServeQueriesUnaffected(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	installCompanion(t, root, "mirror", "roca-mirror", []string{"watch"}, "")
	notices := &safeBuffer{}
	set := startPluginCompanionsWithPolicy(root, data, notices, fastCompanionPolicy)
	defer set.Close()
	waitFor(t, func() bool { return strings.Contains(notices.String(), "plugin mirror companion did not start") })
	if set.alive() {
		t.Fatal("missing companion started a process")
	}
	if strings.Contains(notices.String(), root) || strings.Contains(notices.String(), data) {
		t.Fatalf("notices leaked a local path: %q", notices.String())
	}
	body := companionLog(t, data)
	if !strings.Contains(body, `"event":"unavailable"`) || strings.Contains(body, root) {
		t.Fatalf("telemetry = %s", body)
	}
	svc, err := service.Open(service.Options{DBPath: filepath.Join(data, "roca.db"), DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err := svc.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Health(context.Background(), service.HealthRequest{}); err != nil {
		t.Fatalf("health with a missing companion: %v", err)
	}
}

func TestCompanionCrashRetriesThenStaysDown(t *testing.T) {
	_, starts, notices, set := startCountingCompanion(t, "1")
	defer set.Close()
	waitFor(t, func() bool { return strings.Contains(notices.String(), "plugin mirror companion stopped") })
	waitSettled(t, starts, 3)
	if set.alive() {
		t.Fatal("failed companion was left running")
	}
	if strings.Count(notices.String(), "plugin mirror companion stopped") != 1 {
		t.Fatalf("gave up more than once: %q", notices.String())
	}
}

func TestCompanionCleanExitStopsWithoutCrashTelemetry(t *testing.T) {
	data, starts, notices, set := startCountingCompanion(t, "0")
	defer set.Close()
	waitFor(t, func() bool {
		raw, err := os.ReadFile(starts)
		return err == nil && strings.TrimSpace(string(raw)) == "1"
	})
	time.Sleep(40 * time.Millisecond)
	raw, err := os.ReadFile(starts)
	if err != nil || strings.TrimSpace(string(raw)) != "1" {
		t.Fatalf("clean exit was retried: starts=%s err=%v", raw, err)
	}
	if strings.Contains(notices.String(), "plugin mirror companion stopped") {
		t.Fatalf("clean exit reported as stopped: %q", notices.String())
	}
	body := companionLog(t, data)
	if strings.Contains(body, `"event":"stopped"`) {
		t.Fatalf("clean exit wrote a stopped telemetry record: %s", body)
	}
	if !strings.Contains(body, `"event":"exited"`) || !strings.Contains(body, `"reason":"exit 0"`) {
		t.Fatalf("clean exit telemetry missing exited/exit-0 record: %s", body)
	}
}

// startCountingCompanion installs a companion that counts how many times it
// has started into data/starts and then exits with the given code, raising it
// with the fast test backoff. It returns the data dir, the starts path, the
// notice buffer, and the raised set.
func startCountingCompanion(t *testing.T, exitCode string) (data, starts string, notices *safeBuffer, set *companionSet) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	root, data := t.TempDir(), t.TempDir()
	starts = filepath.Join(data, "starts")
	installCompanion(t, root, "mirror", "roca-mirror", []string{},
		"#!/bin/sh\nn=0\n[ -f "+starts+" ] && n=$(cat "+starts+")\necho $((n+1)) > "+starts+"\nexit "+exitCode+"\n")
	notices = &safeBuffer{}
	set = startPluginCompanionsWithPolicy(root, data, notices, fastCompanionPolicy)
	return data, starts, notices, set
}

func TestCompanionNeverResolvesThroughPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	root, data := t.TempDir(), t.TempDir()
	pathDir := t.TempDir()
	decoy := filepath.Join(pathDir, "roca-mirror")
	marker := filepath.Join(data, "path-hit")
	if err := os.WriteFile(decoy, []byte("#!/bin/sh\necho hit > "+marker+"\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	installCompanion(t, root, "mirror", "roca-mirror", []string{}, "")
	notices := &safeBuffer{}
	set := startPluginCompanionsWithPolicy(root, data, notices, fastCompanionPolicy)
	defer set.Close()
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("companion resolved an executable from PATH")
	}
	if set.alive() {
		t.Fatal("PATH decoy was started")
	}
}

func TestCompanionArgsAreNotInterpretedByAShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	root, data := t.TempDir(), t.TempDir()
	got := filepath.Join(data, "args")
	installCompanion(t, root, "mirror", "roca-mirror", []string{"watch", "a; touch pwned"},
		"#!/bin/sh\nprintf '%s\\n' \"$@\" > "+got+"\nwhile IFS= read -r line; do :; done\n")
	set := startPluginCompanionsWithPolicy(root, data, &safeBuffer{}, fastCompanionPolicy)
	defer set.Close()
	waitFor(t, func() bool {
		raw, err := os.ReadFile(got)
		return err == nil && strings.Contains(string(raw), "a; touch pwned")
	})
	if _, err := os.Stat(filepath.Join(data, "pwned")); err == nil {
		t.Fatal("companion args were interpreted by a shell")
	}
}

func TestConcurrentCompanionsLeaveSingleFlightToThePlugin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	root, data := t.TempDir(), t.TempDir()
	lock := filepath.Join(data, "lock")
	installCompanion(t, root, "mirror", "roca-mirror", []string{},
		"#!/bin/sh\nif ! mkdir "+lock+" 2>/dev/null; then echo $$ > "+data+"/standby; while IFS= read -r line; do :; done; exit 0; fi\n"+
			"echo $$ > "+data+"/holder\ntrap 'rmdir "+lock+"' EXIT\nwhile IFS= read -r line; do :; done\n")
	first := startPluginCompanionsWithPolicy(root, data, &safeBuffer{}, fastCompanionPolicy)
	defer first.Close()
	waitFor(t, func() bool {
		_, err := os.Stat(filepath.Join(data, "holder"))
		return err == nil
	})
	second := startPluginCompanionsWithPolicy(root, data, &safeBuffer{}, fastCompanionPolicy)
	defer second.Close()
	waitFor(t, func() bool {
		_, err := os.Stat(filepath.Join(data, "standby"))
		return err == nil
	})
	if !first.alive() || !second.alive() {
		t.Fatal("concurrent sessions did not both raise a companion")
	}
	if pids := companionPIDs(t, data); len(pids) != 2 {
		t.Fatalf("concurrent companions = %v, want two distinct processes", pids)
	}
}

func companionPIDs(t *testing.T, data string) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, name := range []string{"holder", "standby"} {
		raw, err := os.ReadFile(filepath.Join(data, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		pid := strings.TrimSpace(string(raw))
		if pid == "" || seen[pid] {
			t.Fatalf("%s pid %q is missing or duplicated", name, pid)
		}
		seen[pid] = true
	}
	out := make([]string, 0, len(seen))
	for pid := range seen {
		out = append(out, pid)
	}
	return out
}

func TestCompanionMirrorsAWriteThenDiesWithServe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	root, data := t.TempDir(), t.TempDir()
	watch := filepath.Join(data, "home")
	out := filepath.Join(data, "mirror.db")
	if err := os.Mkdir(watch, 0o700); err != nil {
		t.Fatal(err)
	}
	installCompanion(t, root, "mirror", "roca-mirror", []string{watch, out},
		"#!/bin/sh\nwatch=$1\nout=$2\n( while :; do\n  if [ -f \"$watch/note.md\" ]; then cat \"$watch/note.md\" > \"$out\"; fi\n  sleep 0.05\ndone ) &\nkid=$!\ntrap 'kill $kid' EXIT\nwhile IFS= read -r line; do :; done\n")
	set := startPluginCompanionsWithPolicy(root, data, &safeBuffer{}, fastCompanionPolicy)
	if err := os.WriteFile(filepath.Join(watch, "note.md"), []byte("harbor lantern\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		raw, err := os.ReadFile(out)
		return err == nil && strings.Contains(string(raw), "harbor lantern")
	})
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if set.alive() {
		t.Fatal("companion outlived session close")
	}
}

var fastCompanionPolicy = companionPolicy{
	Backoff: []time.Duration{time.Millisecond, 2 * time.Millisecond},
}

func installCompanion(t *testing.T, root, name, executable string, args []string, body string) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	encodedArgs := "[]"
	if len(args) > 0 {
		quoted := make([]string, len(args))
		for i, arg := range args {
			quoted[i] = `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
		}
		encodedArgs = "[" + strings.Join(quoted, ", ") + "]"
	}
	manifest := `{
  "schema": 1,
  "name": "` + name + `",
  "version": "1.0.0",
  "binary": "` + executable + `",
  "databases": [{
    "name": "records",
    "path": "records.db",
    "alias": "plugin_` + strings.ReplaceAll(name, "-", "_") + `_records",
    "attachment": "resident",
    "retention": "The plugin retains every synthetic record."
  }],
  "semantic": {"databases": [{
    "database": "records",
    "description": "Synthetic records.",
    "questions": ["Which synthetic records exist?"],
    "tables": [{"name": "records", "description": "One synthetic record.", "columns": ["id"]}]
  }]},
  "companion": {"executable": "` + executable + `", "args": ` + encodedArgs + `},
  "verbs": [],
  "capabilities": []
}
`
	if err := os.WriteFile(filepath.Join(directory, plugin.PackageFilename), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if body == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(directory, executable), []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func companionLog(t *testing.T, dataDir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dataDir, logfile.DirName, logfile.Companions+"-*.jsonl"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("companion telemetry files=%v err=%v", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func waitSettled(t *testing.T, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(raw)) == "3" {
			time.Sleep(40 * time.Millisecond)
			raw, err = os.ReadFile(path)
			if err != nil || strings.TrimSpace(string(raw)) != "3" {
				t.Fatalf("starts kept growing: %s", raw)
			}
			if want != 3 {
				t.Fatalf("want %d", want)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("starts never settled")
}

func (s *companionSet) alive() bool {
	if s == nil {
		return false
	}
	return s.running()
}

type safeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
