package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
)

// TestMigratePrintsStagesAndHonoursJSONStreams pins issue #455's output
// contract: text mode prints a stage line per phase beside the final verdict,
// while --json keeps stdout a single JSON document and mirrors progress on
// stderr.
func TestMigratePrintsStagesAndHonoursJSONStreams(t *testing.T) {
	for _, probe := range []struct {
		name       string
		args       []string
		onStdout   string
		onStderr   string
		jsonStdout bool
	}{
		{name: "text", args: []string{"migrate"},
			onStdout: "stage=data2-", onStderr: ""},
		{name: "json", args: []string{"migrate", "--json"},
			onStdout: "\"verified\": true", onStderr: "stage=data2-", jsonStdout: true},
	} {
		t.Run(probe.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			corePath := filepath.Join(home, "roca.db")
			seedLayoutMemory(t, corePath, "migrate progress marker")
			var out, errOut bytes.Buffer
			env := &cliEnv{dbPath: corePath, out: &out, errOut: &errOut,
				build: Build{Version: "v-test", Commit: "fixture"}}
			code, err := executeWithEnv(env, append([]string{"--db-path", corePath}, probe.args...), nil)
			if err != nil || code != 0 {
				t.Fatalf("migrate: code=%d err=%v out=%s errOut=%s", code, err, out.String(), errOut.String())
			}
			if !strings.Contains(out.String(), probe.onStdout) {
				t.Fatalf("stdout %q lacks %q", out.String(), probe.onStdout)
			}
			if probe.onStderr != "" && !strings.Contains(errOut.String(), probe.onStderr) {
				t.Fatalf("stderr %q lacks %q", errOut.String(), probe.onStderr)
			}
			if probe.jsonStdout {
				if stage := strings.Index(out.String(), "stage="); stage >= 0 {
					t.Fatalf("progress leaked onto json stdout: %q", out.String())
				}
				var document struct {
					Verified bool `json:"verified"`
				}
				if err := json.Unmarshal(out.Bytes(), &document); err != nil || !document.Verified {
					t.Fatalf("json stdout = %q err=%v", out.String(), err)
				}
			}
		})
	}
}

// TestMigrateStatusReportsLedgerReadOnly pins the read-only ledger report:
// --status answers beside an unfinished or finished ledger without taking the
// migration lock, and even under --read-only's refusal regime.
func TestMigrateStatusReportsLedgerReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	corePath := filepath.Join(home, "roca.db")
	seedLayoutMemory(t, corePath, "migrate status marker")
	env := migratedCLIEnv(t, corePath)

	var out bytes.Buffer
	env.out = &out
	env.errOut = io.Discard
	if code, err := executeWithEnv(env, []string{"--db-path", corePath, "migrate", "--status"}, nil); err != nil || code != 0 {
		t.Fatalf("status: code=%d err=%v", code, err)
	}
	if !strings.Contains(out.String(), "data2-memory-custody state=verified") ||
		!strings.Contains(out.String(), "memberships=") {
		t.Fatalf("status text = %q", out.String())
	}

	out.Reset()
	env.forceReadOnly = true
	if code, err := executeWithEnv(env, []string{"--db-path", corePath, "migrate", "--status"}, nil); err != nil || code != 0 {
		t.Fatalf("read-only status: code=%d err=%v", code, err)
	}
	env.forceReadOnly = false

	var jsonOut bytes.Buffer
	env.out = &jsonOut
	if code, err := executeWithEnv(env, []string{"--db-path", corePath, "migrate", "--status", "--json"}, nil); err != nil || code != 0 {
		t.Fatalf("json status: code=%d err=%v", code, err)
	}
	var document struct {
		Ledgers []struct {
			Plugin     string `json:"plugin"`
			Present    bool   `json:"present"`
			Migrations []struct {
				Migration string `json:"migration"`
				State     string `json:"state"`
			} `json:"migrations"`
		} `json:"ledgers"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &document); err != nil {
		t.Fatalf("json status = %q err=%v", jsonOut.String(), err)
	}
	found := false
	for _, ledger := range document.Ledgers {
		for _, migration := range ledger.Migrations {
			if migration.Migration == "data2-memory-custody" && migration.State == "verified" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("json status = %q lacks data2 verification", jsonOut.String())
	}
}

func TestMigrateStatusReportsAnAbsentLedger(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	corePath := filepath.Join(home, "roca.db")
	seedLayoutMemory(t, corePath, "absent ledger marker")
	var out bytes.Buffer
	env := &cliEnv{dbPath: corePath, out: &out, errOut: io.Discard,
		build: Build{Version: "v-test", Commit: "fixture"}}
	if code, err := executeWithEnv(env, []string{"--db-path", corePath, "migrate", "--status"}, nil); err != nil || code != 0 {
		t.Fatalf("status before plugins exist: code=%d err=%v", code, err)
	}
	if !strings.Contains(out.String(), rocaops.Name+": absent") {
		t.Fatalf("absent ledger status = %q", out.String())
	}
}
