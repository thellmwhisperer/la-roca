package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/store"
)

type sshCall struct {
	target string
	args   []string
}

type scriptedSSHRunner struct {
	calls   []sshCall
	replies []sshReply
}

type concurrentSSHRunner struct {
	mu           sync.Mutex
	calls        []sshCall
	versionCalls int
	ready        chan struct{}
}

func (runner *concurrentSSHRunner) Run(ctx context.Context, target string, args []string) sshReply {
	runner.mu.Lock()
	runner.calls = append(runner.calls, sshCall{target: target, args: slices.Clone(args)})
	isVersion := len(args) > 1 && args[1] == "version"
	if isVersion {
		runner.versionCalls++
		if runner.versionCalls == 2 {
			close(runner.ready)
		}
	}
	ready := runner.ready
	runner.mu.Unlock()
	if isVersion {
		select {
		case <-ready:
			return remoteVersionReply("v-test")
		case <-ctx.Done():
			return sshReply{err: ctx.Err()}
		case <-time.After(time.Second):
			return sshReply{err: errors.New("remote legs did not start concurrently")}
		}
	}
	answer := 11
	if strings.Contains(target, "beta") {
		answer = 12
	}
	return sshReply{stdout: fmt.Sprintf(`{"sql":"SELECT 7 AS answer","columns":["answer"],"rows":[{"answer":%d}],"row_count":1,"latency_ms":1,"version":"v-test","source_sha":"remote-sha"}`, answer)}
}

func (runner *scriptedSSHRunner) Run(_ context.Context, target string, args []string) sshReply {
	runner.calls = append(runner.calls, sshCall{target: target, args: slices.Clone(args)})
	if len(runner.replies) == 0 {
		return sshReply{err: errors.New("unexpected ssh call")}
	}
	reply := runner.replies[0]
	runner.replies = runner.replies[1:]
	return reply
}

func remoteVersionReply(version string) sshReply {
	body, _ := json.Marshal(map[string]any{"version": version, "source_sha": "remote-sha"})
	return sshReply{stdout: string(body)}
}

func malformedRemoteEnvelopeEnv(t *testing.T, body string) *cliEnv {
	t.Helper()
	home := t.TempDir()
	addRemote(t, home, "studio", "dev@example.test")
	return &cliEnv{sshRunner: &scriptedSSHRunner{replies: []sshReply{
		remoteVersionReply("v-test"), {stdout: body},
	}}}
}

func assertSSHCalls(t *testing.T, got, want []sshCall) {
	t.Helper()
	if !slices.EqualFunc(got, want, func(a, b sshCall) bool {
		return a.target == b.target && slices.Equal(a.args, b.args)
	}) {
		t.Fatalf("ssh calls = %#v, want %#v", got, want)
	}
}

func runRemoteRoot(t *testing.T, env *cliEnv, args ...string) (string, error) {
	t.Helper()
	var output strings.Builder
	env.out, env.errOut = &output, &output
	env.build = Build{Version: "v-test", Commit: "local-sha"}
	env.skipReconciliation = true
	env.featuresLoaded = true
	root := rootCommand(env)
	root.SetArgs(args)
	err := root.Execute()
	return strings.TrimSpace(output.String()), err
}

func addRemote(t *testing.T, home, name, target string) {
	t.Helper()
	t.Setenv("HOME", home)
	if output, err := runRemoteRoot(t, &cliEnv{}, "remote", "add", name, "--ssh", target); err != nil {
		t.Fatalf("add remote: %v\n%s", err, output)
	}
}

func TestRemoteAddGetOrCreatesARegistryAndListPrintsIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	output, err := runRemoteRoot(t, &cliEnv{}, "remote", "add", "studio", "--ssh", "dev@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "remote studio added") || !strings.Contains(output, "help[") {
		t.Fatalf("remote add output:\n%s", output)
	}
	registryPath := filepath.Join(home, ".roca", "remotes.json")
	info, err := os.Stat(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("registry mode = %o, want 600", info.Mode().Perm())
	}

	output, err = runRemoteRoot(t, &cliEnv{}, "remote", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "remotes[1]{name,ssh}:") ||
		!strings.Contains(output, "studio,dev@example.test") || !strings.Contains(output, "help[") {
		t.Fatalf("remote list output:\n%s", output)
	}

	doc := mustJSON(t, runRemoteJSON(t, &cliEnv{}, "remote", "list", "--json"))
	if doc["version"] != "v-test" || doc["source_sha"] != "local-sha" {
		t.Fatalf("remote list envelope = %#v", doc)
	}
	remotes, _ := doc["remotes"].([]any)
	if len(remotes) != 1 || remotes[0].(map[string]any)["name"] != "studio" {
		t.Fatalf("remote list JSON = %#v", doc)
	}
}

func TestRemoteNamesCannotCollideInSQLite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := runRemoteRoot(t, &cliEnv{}, "remote", "add", "LOCAL", "--ssh", "dev@example.test"); err == nil {
		t.Fatal("LOCAL remote was accepted")
	}
	if _, err := runRemoteRoot(t, &cliEnv{}, "remote", "add", "Studio", "--ssh", "dev@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := runRemoteRoot(t, &cliEnv{}, "remote", "add", "studio", "--ssh", "other@example.test"); err == nil || !strings.Contains(err.Error(), "collides case-insensitively") {
		t.Fatalf("case-colliding add error = %v", err)
	}
	if _, err := remoteNames("Studio,studio"); err == nil || !strings.Contains(err.Error(), "case-colliding") {
		t.Fatalf("case-colliding --on error = %v", err)
	}
}

func runRemoteJSON(t *testing.T, env *cliEnv, args ...string) string {
	t.Helper()
	output, err := runRemoteRoot(t, env, args...)
	if err != nil {
		t.Fatalf("roca %v: %v\n%s", args, err, output)
	}
	return output
}

func TestRemoteExecBridgesTheStandardEnvelopeAndInjectsTheSSHRunner(t *testing.T) {
	home := t.TempDir()
	addRemote(t, home, "studio", "dev@example.test")
	runner := &scriptedSSHRunner{replies: []sshReply{
		remoteVersionReply("v-test"),
		{stdout: `{"sql":"SELECT 7 AS answer","columns":["answer"],"rows":[{"answer":7}],"row_count":1,"latency_ms":2,"version":"v-test","source_sha":"remote-sha"}`},
	}}

	output, err := runRemoteRoot(t, &cliEnv{sshRunner: runner}, "remote", "exec", "studio", "SELECT 7 AS answer")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "SELECT 7 AS answer") || !strings.Contains(output, "7") ||
		!strings.Contains(output, "help[") {
		t.Fatalf("remote exec output:\n%s", output)
	}
	wantCalls := []sshCall{
		{target: "dev@example.test", args: []string{"roca", "version", "--json"}},
		{target: "dev@example.test", args: []string{"roca", "exec", "SELECT 7 AS answer", "--json"}},
	}
	assertSSHCalls(t, runner.calls, wantCalls)

	runner.replies = []sshReply{
		remoteVersionReply("v-test"),
		{stdout: `{"sql":"SELECT 7 AS answer","columns":["answer"],"rows":[{"answer":7}],"row_count":1,"latency_ms":2,"version":"v-test","source_sha":"remote-sha"}`},
	}
	doc := mustJSON(t, runRemoteJSON(t, &cliEnv{sshRunner: runner},
		"remote", "exec", "studio", "SELECT 7 AS answer", "--json"))
	if doc["row_count"] != float64(1) || doc["version"] != "v-test" {
		t.Fatalf("remote exec JSON = %#v", doc)
	}
}

func TestRemoteExecLeavesReadOnlyRefusalToTheRemoteGate(t *testing.T) {
	home := t.TempDir()
	addRemote(t, home, "studio", "dev@example.test")
	runner := &scriptedSSHRunner{replies: []sshReply{
		remoteVersionReply("v-test"),
		{stderr: "error: only SELECT statements are allowed", exitCode: 1},
	}}
	env := &cliEnv{sshRunner: runner}
	output, err := runRemoteRoot(t, env, "remote", "exec", "studio", "DELETE FROM memories")
	if err == nil || env.code != ExitError || !strings.Contains(err.Error(), "only SELECT statements are allowed") {
		t.Fatalf("remote gate refusal = code %d err %v output %q", env.code, err, output)
	}
}

func TestRemoteExecRejectsMalformedEnvelopesAsSkew(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "null", body: `null`},
		{name: "missing fields", body: `{}`},
		{name: "missing version", body: `{"sql":"SELECT 1","columns":["n"],"rows":[{"n":1}],"row_count":1,"latency_ms":1,"source_sha":"sha"}`},
		{name: "row count mismatch", body: `{"sql":"SELECT 1","columns":["n"],"rows":[{"n":1}],"row_count":2,"latency_ms":1,"version":"v-test","source_sha":"sha"}`},
		{name: "row shape mismatch", body: `{"sql":"SELECT 1","columns":["n"],"rows":[{"other":1}],"row_count":1,"latency_ms":1,"version":"v-test","source_sha":"sha"}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			env := malformedRemoteEnvelopeEnv(t, testCase.body)
			_, err := runRemoteRoot(t, env, "remote", "exec", "studio", "SELECT 1")
			if err == nil || env.code != ExitRemoteVersionSkew ||
				!strings.Contains(err.Error(), "incompatible exec envelope") {
				t.Fatalf("malformed exec envelope = code %d err %v", env.code, err)
			}
		})
	}
}

func TestRemoteVectorQueryBridgesResultsAndPropagatesAnAbsentIndexHonestly(t *testing.T) {
	home := t.TempDir()
	addRemote(t, home, "studio", "dev@example.test")
	runner := &scriptedSSHRunner{replies: []sshReply{
		remoteVersionReply("v-test"),
		{stdout: `{"query":"remembered decision","k":3,"model":"embed-test","results":[{"rank":1,"score":0.9,"database":"corpus","table":"memories","id":"4","source":"memories","source_id":"memories/4","text":"A remembered decision"}],"elapsed_ms":4}`},
	}}
	output, err := runRemoteRoot(t, &cliEnv{sshRunner: runner},
		"remote", "vector", "query", "studio", "remembered decision", "3")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "rows[1]") || !strings.Contains(output, "A remembered decision") ||
		!strings.Contains(output, "help[") {
		t.Fatalf("remote vector output:\n%s", output)
	}

	runner = &scriptedSSHRunner{replies: []sshReply{
		remoteVersionReply("v-test"),
		{stderr: "error: vector index is not ready; run `roca vector install`", exitCode: 1},
	}}
	env := &cliEnv{sshRunner: runner}
	_, err = runRemoteRoot(t, env, "remote", "vector", "query", "studio", "remembered decision")
	if err == nil || env.code != ExitError ||
		!strings.Contains(err.Error(), "vector index is not ready; run `roca vector install`") {
		t.Fatalf("absent vector index = code %d err %v", env.code, err)
	}
}

func TestRemoteVectorRejectsMalformedEnvelopesAsSkew(t *testing.T) {
	tests := []string{
		`null`,
		`{}`,
		`{"query":"remembered decision","k":10,"results":[null],"elapsed_ms":1}`,
		`{"query":"remembered decision","k":10,"results":[{"rank":1,"score":0.5,"source":"memories","source_id":"memories/1"}],"elapsed_ms":1}`,
	}
	for index, body := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			env := malformedRemoteEnvelopeEnv(t, body)
			_, err := runRemoteRoot(t, env, "remote", "vector", "query", "studio", "remembered decision")
			if err == nil || env.code != ExitRemoteVersionSkew ||
				!strings.Contains(err.Error(), "incompatible vector envelope") {
				t.Fatalf("malformed vector envelope = code %d err %v", env.code, err)
			}
		})
	}
}

func TestRemoteTransportFailuresHaveDistinctExitCodesAndMessages(t *testing.T) {
	tests := []struct {
		name     string
		reply    sshReply
		wantCode int
		want     string
	}{
		{name: "unreachable", reply: sshReply{stderr: "ssh: connect to host example.test: Connection refused", exitCode: 255},
			wantCode: ExitRemoteUnreachable, want: "remote studio is unreachable"},
		{name: "roca missing", reply: sshReply{stderr: "sh: roca: command not found", exitCode: 127},
			wantCode: ExitRemoteRocaMissing, want: "remote studio does not have roca on PATH"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			home := t.TempDir()
			addRemote(t, home, "studio", "dev@example.test")
			env := &cliEnv{sshRunner: &scriptedSSHRunner{replies: []sshReply{testCase.reply}}}
			_, err := runRemoteRoot(t, env, "remote", "exec", "studio", "SELECT 1")
			if err == nil || env.code != testCase.wantCode || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("failure = code %d err %v, want code %d containing %q",
					env.code, err, testCase.wantCode, testCase.want)
			}
		})
	}
}

func TestRemoteCommandMissingAfterVersionProbeKeepsItsDistinctExitCode(t *testing.T) {
	home := t.TempDir()
	addRemote(t, home, "studio", "dev@example.test")
	env := &cliEnv{sshRunner: &scriptedSSHRunner{replies: []sshReply{
		remoteVersionReply("v-test"),
		{stderr: "sh: roca: command not found", exitCode: 127},
	}}}
	_, err := runRemoteRoot(t, env, "remote", "exec", "studio", "SELECT 1")
	if err == nil || env.code != ExitRemoteRocaMissing ||
		!strings.Contains(err.Error(), "remote studio does not have roca on PATH") {
		t.Fatalf("post-probe failure = code %d err %v", env.code, err)
	}
}

func TestRemoteTransportExitCodesSurviveTheProductionExecutor(t *testing.T) {
	home := t.TempDir()
	addRemote(t, home, "studio", "dev@example.test")
	t.Setenv("ROCA_READ_ONLY", "1")
	var output strings.Builder
	env := hermeticCLIEnv(&cliEnv{
		build: Build{Version: "v-test", Commit: "local-sha"}, out: &output, errOut: &output,
		sshRunner: &scriptedSSHRunner{replies: []sshReply{{
			stderr: "ssh: connect to host example.test: Connection refused", exitCode: 255,
		}}},
	})
	code, err := executeWithEnv(env, []string{"remote", "exec", "studio", "SELECT 1"}, nil)
	if err == nil || code != ExitRemoteUnreachable {
		t.Fatalf("production executor = code %d err %v, want %d", code, err, ExitRemoteUnreachable)
	}
}

func TestRemoteVersionSkewHasItsOwnExitCode(t *testing.T) {
	home := t.TempDir()
	addRemote(t, home, "studio", "dev@example.test")
	env := &cliEnv{sshRunner: &scriptedSSHRunner{replies: []sshReply{remoteVersionReply("v-other")}}}
	_, err := runRemoteRoot(t, env, "remote", "exec", "studio", "SELECT 1")
	if err == nil || env.code != ExitRemoteVersionSkew ||
		!strings.Contains(err.Error(), "remote studio runs roca v-other; local roca is v-test") {
		t.Fatalf("version skew = code %d err %v", env.code, err)
	}
}

func TestRemoteCrossScatterGathersOnlyInMemory(t *testing.T) {
	fixture := fixtureInstallation(t)
	addRemote(t, fixture.home, "studio", "dev@example.test")
	ops, err := store.Open(filepath.Join(fixture.home, ".roca", "plugins",
		rocaops.Name, rocaops.DatabaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer ops.Close()
	if _, err := ops.SQL().Exec(`PRAGMA wal_autocheckpoint=0; INSERT INTO layers
		(name, description, schema_file) VALUES ('remote-cross-marker', 'marker', 'marker')`); err != nil {
		t.Fatal(err)
	}
	core, err := store.Open(filepath.Join(fixture.home, ".roca", "roca.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	if _, err := core.SQL().Exec(`PRAGMA wal_autocheckpoint=0; INSERT INTO memories
		(id, layer, content, origin) VALUES (909, 'project', 'Local WAL marker', 'agent')`); err != nil {
		t.Fatal(err)
	}
	before := treeSnapshot(t, fixture.home)
	runner := &scriptedSSHRunner{replies: []sshReply{
		remoteVersionReply("v-test"),
		{stdout: `{"sql":"SELECT id, content FROM memories WHERE id = 909","columns":["id","content"],"rows":[{"id":909,"content":"Remote marker"}],"row_count":1,"latency_ms":1,"version":"v-test","source_sha":"remote-sha"}`},
	}}
	env := &cliEnv{sshRunner: runner}
	statement := "SELECT id, content FROM memories WHERE id = 909"
	output, err := runRemoteRoot(t, env, "remote", "cross", statement, "--on", "studio")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rows[2]{origin,id,content", "local,909,Local WAL marker", "studio,909,Remote marker", "help["} {
		if !strings.Contains(output, want) {
			t.Errorf("cross output lacks %q:\n%s", want, output)
		}
	}
	wantCalls := []sshCall{
		{target: "dev@example.test", args: []string{"roca", "version", "--json", "--read-only"}},
		{target: "dev@example.test", args: []string{"roca", "exec", statement, "--json", "--read-only"}},
	}
	assertSSHCalls(t, runner.calls, wantCalls)
	after := treeSnapshot(t, fixture.home)
	if !equalTreeSnapshots(before, after) {
		t.Fatalf("cross changed a rock: before=%v after=%v", mapKeys(before), mapKeys(after))
	}
}

func TestRemoteCrossStartsAllRemoteLegsBeforeGatheringInInputOrder(t *testing.T) {
	fixture := fixtureInstallation(t)
	addRemote(t, fixture.home, "alpha", "alpha@example.test")
	addRemote(t, fixture.home, "beta", "beta@example.test")
	runner := &concurrentSSHRunner{ready: make(chan struct{})}
	output, err := runRemoteRoot(t, &cliEnv{sshRunner: runner}, "remote", "cross",
		"SELECT 7 AS answer", "--on", "alpha,beta")
	if err != nil {
		t.Fatal(err)
	}
	local := strings.Index(output, "local,7")
	alpha := strings.Index(output, "alpha,11")
	beta := strings.Index(output, "beta,12")
	if local < 0 || alpha <= local || beta <= alpha {
		t.Fatalf("cross output order:\n%s", output)
	}
}

type treeSnapshotEntry struct {
	mode    fs.FileMode
	modTime int64
	body    []byte
}

func treeSnapshot(t *testing.T, root string) map[string]treeSnapshotEntry {
	t.Helper()
	result := map[string]treeSnapshotEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(rel)
		logDir := filepath.ToSlash(filepath.Join(".roca", logfile.DirName))
		if slash == logDir || filepath.Dir(slash) == logDir &&
			(entry.Name() == ".roca.lock" || strings.HasPrefix(entry.Name(), logfile.Snapshots+"-") &&
				strings.HasSuffix(entry.Name(), ".jsonl")) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := treeSnapshotEntry{mode: info.Mode(), modTime: info.ModTime().UnixNano()}
		if info.Mode().IsRegular() {
			item.body, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		result[path] = item
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func equalTreeSnapshots(left, right map[string]treeSnapshotEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		other, found := right[key]
		if !found || value.mode != other.mode {
			return false
		}
		if value.modTime != other.modTime ||
			!slices.Equal(value.body, other.body) {
			return false
		}
	}
	return true
}

func mapKeys(values map[string]treeSnapshotEntry) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
