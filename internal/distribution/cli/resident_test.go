package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	transport "github.com/thellmwhisperer/la-roca/pkg/resident"
)

func residentCLIStub(t *testing.T, reply func(transport.Request) any) {
	t.Helper()
	dir, err := os.MkdirTemp("", "r439-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "test.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCA_RESIDENT_SOCKET", socket)
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				decoder := json.NewDecoder(conn)
				var req transport.Request
				if decoder.Decode(&req) != nil {
					return
				}
				if req.Op == "connect" {
					_ = json.NewEncoder(conn).Encode(transport.Response{Result: json.RawMessage(`{}`)})
					if decoder.Decode(&req) != nil {
						return
					}
				}
				value := reply(req)
				raw, _ := json.Marshal(value)
				_ = json.NewEncoder(conn).Encode(transport.Response{Result: raw})
			}()
		}
	}()
}

func TestResidentCLIExecPreservesIntegersBudgetsAndAuditDestination(t *testing.T) {
	isolateRuntimeDirs(t, t.TempDir())
	selected := filepath.Join(t.TempDir(), "selected.db")
	text := strings.Repeat("x", 1500)
	residentCLIStub(t, func(req transport.Request) any {
		return service.ExecResult{Columns: []string{"id", "text"}, Rows: []map[string]any{{"id": int64(9007199254740993), "text": text}}, RowCount: 1}
	})
	var out, errs strings.Builder
	code, err := execute(contractBuild(), &out, &errs, []string{"--db-path", selected, "exec", "SELECT 9007199254740993 AS id", "--max-chars", "2000"})
	if err != nil || code != ExitOK {
		t.Fatalf("code=%d err=%v stderr=%s", code, err, &errs)
	}
	if !strings.Contains(out.String(), `"9007199254740993"`) || !strings.Contains(out.String(), text) {
		t.Fatalf("lost precision or budget: %s", &out)
	}
	file, err := os.Open(filepath.Join(filepath.Dir(selected), "logs", logfile.Executions+"-"+time.Now().UTC().Format(time.DateOnly)+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var record logfile.ExecutionRecord
	if err := json.NewDecoder(file).Decode(&record); err != nil {
		t.Fatal(err)
	}
	if record.DatabasePath != selected || record.Command != "exec" {
		t.Fatalf("audit = %+v", record)
	}
}

func TestResidentCLIQueryRetainsBudget(t *testing.T) {
	isolateRuntimeDirs(t, t.TempDir())
	text := strings.Repeat("long snippet ", 100)
	residentCLIStub(t, func(req transport.Request) any {
		return map[string]any{"question": "snippet", "hits": []map[string]any{{"snippet": text}}, "row_count": 1}
	})
	var out strings.Builder
	env := &cliEnv{out: &out, errOut: &out}
	handled, err := env.tryResident(context.Background(), []string{"query", "snippet", "--max-chars", "2000"})
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if !strings.Contains(out.String(), strings.TrimSpace(text)) {
		t.Fatalf("truncated query: %s", &out)
	}
}

func TestResidentCLIStoreMetadataPreservesNumbers(t *testing.T) {
	isolateRuntimeDirs(t, t.TempDir())
	received := make(chan transport.Request, 1)
	residentCLIStub(t, func(req transport.Request) any { received <- req; return service.StoreResult{ID: 1} })
	var out strings.Builder
	env := &cliEnv{out: &out, errOut: &out}
	handled, err := env.tryResident(context.Background(), []string{"store", "--layer", "discovery", "--content", "number", "--metadata", `{"id":9007199254740993}`})
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	var req service.StoreRequest
	if err := decodeJSON(string((<-received).Args), &req); err != nil {
		t.Fatal(err)
	}
	if req.Metadata["id"] != json.Number("9007199254740993") {
		t.Fatalf("metadata=%v", req.Metadata)
	}
}

func TestResidentCLIHandoffUsesExistingValidationAndLimits(t *testing.T) {
	isolateRuntimeDirs(t, t.TempDir())
	residentCLIStub(t, func(req transport.Request) any {
		if req.Op == "handoff_all" {
			return service.HandoffLab{Rows: []service.HandoffLabRow{{Project: "a"}, {Project: "b"}}}
		}
		return service.HandoffList{Project: "a", Handoffs: []service.MemoryRecord{{ID: 1}, {ID: 2}}}
	})
	for _, args := range [][]string{
		{"handoff", "latest", "--project", "a", "--limit", "1", "--json"},
		{"handoff", "latest", "--all-projects", "--limit", "1", "--json"},
	} {
		var out strings.Builder
		code, err := execute(contractBuild(), &out, &out, args)
		if err != nil || code != ExitOK {
			t.Fatalf("%v: %v %s", args, err, &out)
		}
		var result struct {
			Handoffs []json.RawMessage `json:"handoffs"`
			Rows     []json.RawMessage `json:"rows"`
		}
		if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Handoffs)+len(result.Rows) != 1 {
			t.Fatalf("limit ignored: %s", &out)
		}
	}
	for _, args := range [][]string{
		{"handoff", "latest", "--project", "a", "--all-projects"},
		{"handoff", "latest", "--limit", "-1"},
		{"handoff", "latest", "--head-chars", "5"},
	} {
		var out strings.Builder
		if code, err := execute(contractBuild(), &out, &out, args); err == nil || code == ExitOK {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestResidentCLIDoctorReportAndEnrichment(t *testing.T) {
	isolateRuntimeDirs(t, t.TempDir())
	dbPath := setupFreshSupportHome(t, os.Getenv("HOME"))
	residentCLIStub(t, func(req transport.Request) any {
		if req.Op == "status" {
			return transport.Status{PID: 123}
		}
		return service.DoctorReport{}
	})
	writer := logfile.New(filepath.Dir(dbPath))
	if err := writer.Append(logfile.Executions, logfile.ExecutionRecord{CallRecord: logfile.CallRecord{Timestamp: time.Now(), Source: "cli", OK: false, Error: "synthetic query failure"}, Command: "query"}); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(filepath.Dir(dbPath), "logs", logfile.Executions+"-"+time.Now().UTC().Format(time.DateOnly)+".jsonl")
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	code, err := execute(contractBuild(), &out, &out, []string{"doctor", "--report=true", "--db-path", dbPath, "--json"})
	if err != nil || code != ExitOK {
		t.Fatalf("report: %v %s", err, &out)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out.String()), &report); err != nil {
		t.Fatal(err)
	}
	if report["kind"] == nil || strings.Contains(out.String(), dbPath) {
		t.Fatalf("unsafe report: %s", &out)
	}
	after, err := os.ReadFile(logPath)
	if err != nil || string(before) != string(after) {
		t.Fatalf("report wrote audit: %v", err)
	}
	out.Reset()
	code, err = execute(contractBuild(), &out, &out, []string{"doctor", "--db-path", dbPath, "--json"})
	if err != nil || code != ExitOK {
		t.Fatalf("doctor: %v %s", err, &out)
	}
	var result doctorReport
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatal(err)
	}
	if result.QueryFailures.Count != 1 || result.Resident == nil || result.Resident.PID != 123 {
		t.Fatalf("doctor lost enrichment: %s", &out)
	}
	file, err := os.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	count := 0
	for scanner.Scan() {
		count++
	}
	if count != strings.Count(string(before), "\n")+1 {
		t.Fatalf("audit count = %d", count)
	}
}
