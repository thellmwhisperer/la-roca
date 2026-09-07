//go:build acceptance

// @overview Exercise shared residency through real MCP server processes.
// READING GUIDE: TestVectorResidentProcessCountEvidence -> threeServeResidentPS
// -> writeResidentEvidence; the remaining helpers isolate and clean up fixtures.
// MAIN FLOW: initialize home -> connect three servers -> query -> close -> idle exit.
// PUBLIC API: TestVectorResidentProcessCountEvidence runs the acceptance contract.
// INTERNALS: residentEvidence, process observation, fake payload and evidence helpers.
// @exports TestVectorResidentProcessCountEvidence
// @deps MCP client SDK, testfixture process helpers, operating-system interfaces
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/test/testfixture"
)

// -- 1 CORE · TestVectorResidentProcessCountEvidence <- START HERE --
func TestVectorResidentProcessCountEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shared residency is proven on unix sockets")
	}
	fake := buildFakeVectorResident(t)
	t.Run("published", func(t *testing.T) {
		binary := publishedRoca(t)
		evidence := threeServeResidentPS(t, binary, fake, 3*time.Second)
		writeResidentEvidence(t, "published", evidence)
		if evidence.count != 3 {
			t.Fatalf("published binary residents = %d, want 3\n%s", evidence.count, evidence.ps)
		}
	})
	t.Run("branch", func(t *testing.T) {
		binary, err := rocaBinary()
		if err != nil {
			t.Fatal(err)
		}
		evidence := threeServeResidentPS(t, binary, fake, time.Second)
		if evidence.count != 1 {
			t.Fatalf("branch residents = %d, want 1\n%s", evidence.count, evidence.ps)
		}
		var transcript strings.Builder
		fmt.Fprintf(&transcript, "Three real roca mcp serve processes; deterministic resident protocol substitute (no model RSS measurement).\nMCP server PIDs: %v\n", evidence.serverPIDs)
		query := func(i int, session *mcp.ClientSession) {
			t.Helper()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: "roca_vector_query", Arguments: map[string]any{"query": "harbor lantern", "k": 3},
			})
			if err != nil {
				t.Fatalf("session %d vector query: %v", i, err)
			}
			if result == nil || result.IsError {
				t.Fatalf("session %d vector query failed or returned nothing: %#v", i, result)
			}
			got := renderedText(result) + fmt.Sprint(result.Content)
			if !strings.Contains(got, "harbor lantern") {
				t.Fatalf("session %d vector query missed: %#v", i, result)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&transcript, "session %d tools/call roca_vector_query(query=harbor lantern,k=3): %s\n", i, raw)
		}
		for i, session := range evidence.sessions {
			query(i, session)
		}
		if err := evidence.sessions[0].Close(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&transcript, "Closed session 0; querying session 1 again:")
		query(1, evidence.sessions[1])
		afterClose := testfixture.ResidentPS(t, evidence.hint)
		if afterClose != evidence.ps {
			t.Fatalf("closing one MCP session changed the resident process:\nbefore: %s\nafter: %s", evidence.ps, afterClose)
		}
		fmt.Fprintf(&transcript, "Same resident after closing session 0:\n%s\n", afterClose)
		for _, session := range evidence.sessions {
			_ = session.Close()
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			psOutput := testfixture.ResidentPS(t, evidence.hint)
			if testfixture.CountResidentLines(psOutput) == 0 {
				fmt.Fprintln(&transcript, "Closed all sessions; ps reports 0 matching resident processes after idle.")
				evidence.transcript = transcript.String()
				writeResidentEvidence(t, "branch", evidence)
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("branch resident still alive after idle\n%s", psOutput)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
}

// -/ 1

// -- 2 HELPER · Isolated servers and payload --
type residentEvidence struct {
	ps         string
	count      int
	hint       string
	sessions   []*mcp.ClientSession
	serverPIDs []int
	transcript string
}

func threeServeResidentPS(t *testing.T, binary, fake string, idle time.Duration) residentEvidence {
	t.Helper()
	m := aWorldIn(t, "vector-share")
	m.binary = binary
	if err := m.runInit(); err != nil {
		t.Fatalf("init: %v\n%s", err, m.last.stderr)
	}
	if m.last.code != 0 {
		t.Fatalf("init: code %d\n%s", m.last.code, m.last.stderr)
	}
	if err := enableVectorFeature(m.home); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "rv-acc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "resident.sock")
	t.Cleanup(func() {
		testfixture.KillResidents(socket)
		_ = os.Remove(socket)
		_ = os.Remove(socket + ".lock")
	})
	env := append(m.environment(),
		"ROCA_VECTOR_RESIDENT_BINARY="+fake,
		"ROCA_VECTOR_RESIDENT_SOCKET="+socket,
		"ROCA_VECTOR_RESIDENT_IDLE="+idle.String(),
	)
	sessions := make([]*mcp.ClientSession, 3)
	serverPIDs := make([]int, 3)
	for i := 0; i < 3; i++ {
		command := exec.Command(binary, "mcp", "serve")
		command.Env = env
		command.Stderr = os.Stderr
		client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "1"}, nil)
		session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: command}, nil)
		if err != nil {
			t.Fatalf("mcp serve %d: %v", i, err)
		}
		t.Cleanup(func() { _ = session.Close() })
		sessions[i] = session
		serverPIDs[i] = command.Process.Pid
	}
	time.Sleep(300 * time.Millisecond)
	hint := m.home
	psOutput := testfixture.ResidentPS(t, hint)
	if testfixture.CountResidentLines(psOutput) == 0 {
		psOutput = testfixture.ResidentPS(t, socket)
		hint = socket
	}
	return residentEvidence{ps: psOutput, count: testfixture.CountResidentLines(psOutput), hint: hint, sessions: sessions, serverPIDs: serverPIDs}
}

func publishedRoca(t *testing.T) string {
	t.Helper()
	if path := strings.TrimSpace(os.Getenv("ROCA_PUBLISHED_BIN")); path != "" {
		return path
	}
	t.Skip("set ROCA_PUBLISHED_BIN to an explicit pre-fix binary for the historical comparison")
	return ""
}

func buildFakeVectorResident(t *testing.T) string {
	t.Helper()
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "roca-vector")
	command := exec.Command("go", "build", "-o", out, "./testdata/fake-vector-resident")
	command.Dir = filepath.Join(root, "test", "acceptance")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake vector resident: %v\n%s", err, output)
	}
	return out
}

func enableVectorFeature(home string) error {
	path := filepath.Join(home, ".roca", "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	body := string(raw)
	if strings.Contains(body, "vector = true") {
		return nil
	}
	if strings.Contains(body, "vector = false") {
		body = strings.Replace(body, "vector = false", "vector = true", 1)
	} else if strings.Contains(body, "[features]") {
		body = strings.Replace(body, "[features]", "[features]\nvector = true", 1)
	} else {
		body += "\n[features]\nvector = true\n"
	}
	return os.WriteFile(path, []byte(body), 0o600)
}

// -/ 2

// -- 3 HELPER · Evidence output --
func writeResidentEvidence(t *testing.T, name string, evidence residentEvidence) {
	t.Helper()
	body := fmt.Sprintf("point: %s\ncount: %d\nps:\n%s\n%s", name, evidence.count, evidence.ps, evidence.transcript)
	t.Logf("issue 315 %s evidence:\n%s", name, body)
	dir := os.Getenv("ROCA_RESIDENT_EVIDENCE_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+"-ps.txt")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// -/ 3
