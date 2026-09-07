//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
		writeResidentEvidence(t, "branch", evidence)
		if evidence.count != 1 {
			t.Fatalf("branch residents = %d, want 1\n%s", evidence.count, evidence.ps)
		}
		for i, session := range evidence.sessions {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "roca_vector_query", Arguments: map[string]any{"query": "harbor lantern", "k": 3},
			})
			if err != nil {
				t.Fatalf("session %d vector query: %v", i, err)
			}
			if result == nil {
				t.Fatalf("session %d vector query returned nothing", i)
			}
			got := renderedText(result) + fmt.Sprint(result.Content)
			if !strings.Contains(got, "harbor lantern") {
				t.Fatalf("session %d vector query missed: %#v", i, result)
			}
		}
		for _, session := range evidence.sessions {
			_ = session.Close()
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			psOutput := residentPS(t, evidence.hint)
			if countResidentLines(psOutput) == 0 {
				t.Logf("branch resident gone after idle:\n%s", psOutput)
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("branch resident still alive after idle\n%s", psOutput)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
}

type residentEvidence struct {
	ps       string
	count    int
	hint     string
	sessions []*mcp.ClientSession
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
		killResidents(socket)
		_ = os.Remove(socket)
		_ = os.Remove(socket + ".lock")
	})
	env := append(m.environment(),
		"ROCA_VECTOR_RESIDENT_BINARY="+fake,
		"ROCA_VECTOR_RESIDENT_SOCKET="+socket,
		"ROCA_VECTOR_RESIDENT_IDLE="+idle.String(),
	)
	sessions := make([]*mcp.ClientSession, 3)
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
	}
	time.Sleep(300 * time.Millisecond)
	hint := m.home
	psOutput := residentPS(t, hint)
	if countResidentLines(psOutput) == 0 {
		psOutput = residentPS(t, socket)
		hint = socket
	}
	return residentEvidence{ps: psOutput, count: countResidentLines(psOutput), hint: hint, sessions: sessions}
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

func writeResidentEvidence(t *testing.T, name string, evidence residentEvidence) {
	t.Helper()
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".tmp", "issue-315")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("point: %s\ncount: %d\nps:\n%s\n", name, evidence.count, evidence.ps)
	path := filepath.Join(dir, name+"-ps.txt")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("issue 315 %s evidence (%s):\n%s", name, path, body)
}

func residentPS(t *testing.T, hint string) string {
	t.Helper()
	output, err := exec.Command("ps", "-ax", "-o", "pid=,args=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "_resident") && strings.Contains(line, hint) {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	return strings.Join(lines, "\n")
}

func countResidentLines(psOutput string) int {
	if strings.TrimSpace(psOutput) == "" {
		return 0
	}
	count := 0
	for _, line := range strings.Split(psOutput, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func killResidents(hint string) {
	output, err := exec.Command("ps", "-ax", "-o", "pid=,args=").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.Contains(line, "_resident") || !strings.Contains(line, hint) {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Kill()
		}
	}
}
