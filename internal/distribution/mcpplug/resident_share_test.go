package mcpplug

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestMain(m *testing.M) {
	if isFakeResidentInvocation() {
		os.Exit(runFakeResident())
	}
	os.Exit(m.Run())
}

func isFakeResidentInvocation() bool {
	for _, arg := range os.Args[1:] {
		if arg == "_resident" {
			return true
		}
	}
	return false
}

func runFakeResident() int {
	listen := ""
	idle := 2 * time.Second
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--listen":
			i++
			if i < len(args) {
				listen = args[i]
			}
		case "--idle":
			i++
			if i < len(args) {
				if parsed, err := time.ParseDuration(args[i]); err == nil && parsed > 0 {
					idle = parsed
				}
			}
		case "--db-path", "--state-dir":
			i++
		}
	}
	if envIdle := strings.TrimSpace(os.Getenv("ROCA_VECTOR_RESIDENT_IDLE")); envIdle != "" {
		if parsed, err := time.ParseDuration(envIdle); err == nil && parsed > 0 {
			idle = parsed
		}
	}
	if listen == "" {
		return runFakeStdioResident()
	}
	return runFakeListenResident(listen, idle)
}

func runFakeStdioResident() int {
	serveFakeResidentSession(os.Stdin, os.Stdout)
	return 0
}

func runFakeListenResident(socket string, idle time.Duration) int {
	if conn, err := net.DialTimeout("unix", socket, 200*time.Millisecond); err == nil {
		_ = conn.Close()
		return 0
	}
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer listener.Close()
	defer os.Remove(socket)
	var (
		clients int
		mu      sync.Mutex
		timer   *time.Timer
		done    = make(chan struct{})
		once    sync.Once
	)
	stop := func() { once.Do(func() { close(done); listener.Close() }) }
	arm := func() {
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(idle, func() {
			mu.Lock()
			n := clients
			mu.Unlock()
			if n == 0 {
				stop()
			}
		})
	}
	arm()
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-done:
				return 0
			default:
				return 1
			}
		}
		mu.Lock()
		clients++
		if timer != nil {
			timer.Stop()
			timer = nil
		}
		mu.Unlock()
		go func() {
			defer func() {
				_ = conn.Close()
				mu.Lock()
				clients--
				n := clients
				mu.Unlock()
				if n == 0 {
					arm()
				}
			}()
			serveFakeResidentSession(conn, conn)
		}()
	}
}

func serveFakeResidentSession(in io.Reader, out io.Writer) {
	encoder := json.NewEncoder(out)
	_ = encoder.Encode(map[string]any{
		"kind": "progress", "stage": "prewarm", "message": "semantic search: preparing",
	})
	_ = encoder.Encode(map[string]any{
		"kind": "result", "stage": "prewarm", "message": "semantic search: ready",
		"extra": map[string]any{"prewarm_ms": 1},
	})
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var request struct {
			ID    int64  `json:"id"`
			Query string `json:"query"`
			K     int    `json:"k"`
		}
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			_ = encoder.Encode(map[string]any{"kind": "error", "stage": "query", "error": err.Error()})
			continue
		}
		_ = encoder.Encode(map[string]any{
			"kind": "result", "stage": "query", "id": request.ID,
			"result": map[string]any{"hit": request.Query, "k": request.K},
		})
	}
}

func TestThreeSessionsShareOneResident(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shared residency is proven on unix sockets")
	}
	svc := sharedResidentLab(t)
	var group sync.WaitGroup
	residents := make([]*residentVector, 3)
	errs := make([]error, 3)
	group.Add(3)
	for i := 0; i < 3; i++ {
		go func(i int) {
			defer group.Done()
			residents[i], errs[i] = startResidentVector(context.Background(), svc)
		}(i)
	}
	group.Wait()
	t.Cleanup(func() {
		for _, resident := range residents {
			if resident != nil {
				_ = resident.Close()
			}
		}
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
	}
	psOutput := residentPS(t, labHint(svc))
	t.Logf("shared resident ps:\n%s", psOutput)
	if count := countResidentLines(psOutput); count != 1 {
		t.Fatalf("resident processes = %d, want 1\n%s", count, psOutput)
	}
	for i, resident := range residents {
		result, _, err := resident.call(context.Background(), nil, vectorQueryArgs{Query: "harbor lantern", K: 3})
		if err != nil {
			t.Fatalf("session %d query: %v", i, err)
		}
		if result == nil || len(result.Content) == 0 {
			t.Fatalf("session %d returned no content", i)
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok || !strings.Contains(text.Text, "harbor lantern") {
			t.Fatalf("session %d result = %#v", i, result.Content)
		}
	}
}

func TestStaleResidentSocketDoesNotWedgeSpawn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shared residency is proven on unix sockets")
	}
	svc := sharedResidentLab(t)
	socket, _ := residentSocketPaths(svc)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	resident, err := startResidentVector(context.Background(), svc)
	if err != nil {
		t.Fatalf("stale socket wedged spawn: %v", err)
	}
	t.Cleanup(func() { _ = resident.Close() })
	if err := resident.waitReady(context.Background()); err != nil {
		t.Fatalf("stale socket resident was not ready: %v", err)
	}
}

func TestResidentCloseDoesNotKillSharedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shared residency is proven on unix sockets")
	}
	svc := sharedResidentLab(t)
	first, err := startResidentVector(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.waitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := startResidentVector(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	result, _, err := second.call(context.Background(), nil, vectorQueryArgs{Query: "harbor lantern"})
	if err != nil {
		t.Fatalf("query after first session closed: %v", err)
	}
	if result == nil {
		t.Fatal("query after first session closed returned nothing")
	}
	psOutput := residentPS(t, labHint(svc))
	t.Logf("resident after first close:\n%s", psOutput)
	if count := countResidentLines(psOutput); count != 1 {
		t.Fatalf("resident processes = %d, want 1\n%s", count, psOutput)
	}
}

func TestSharedResidentExitsAfterIdleOnceClientsLeave(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shared residency is proven on unix sockets")
	}
	svc := sharedResidentLab(t)
	t.Setenv("ROCA_VECTOR_RESIDENT_IDLE", "200ms")
	first, err := startResidentVector(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.waitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		psOutput := residentPS(t, labHint(svc))
		if countResidentLines(psOutput) == 0 {
			t.Logf("resident gone after idle:\n%s", psOutput)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("resident still alive after idle\n%s", psOutput)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func sharedResidentLab(t *testing.T) *service.Service {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shared residency is proven on unix sockets")
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	data := filepath.Join(home, ".roca")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCA_VECTOR_RESIDENT_BINARY", exe)
	socket := filepath.Join("/tmp", fmt.Sprintf("rv-%d.sock", time.Now().UnixNano()))
	t.Setenv("ROCA_VECTOR_RESIDENT_SOCKET", socket)
	t.Setenv("ROCA_VECTOR_RESIDENT_IDLE", "2s")
	svc, err := service.Open(service.Options{
		DBPath: filepath.Join(data, "roca.db"), DataDir: data, VectorEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		svc.Close()
		killResidents(socket)
		_ = os.Remove(socket)
		_ = os.Remove(socket + ".lock")
	})
	return svc
}

func labHint(svc *service.Service) string {
	socket, _ := residentSocketPaths(svc)
	return socket
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
		proc, err := os.FindProcess(pid)
		if err == nil {
			_ = proc.Kill()
		}
	}
}
