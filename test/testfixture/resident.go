// @overview Shared fake vector resident and process helpers for unit and acceptance suites.
// READING GUIDE: RunFakeResident -> runFakeListenResident -> serveFakeResidentSession.
// MAIN FLOW: parse invocation -> serve stdio or socket sessions -> exit after idle.
// PUBLIC API: RunFakeResident serves the fake protocol; ResidentPS observes matching
// processes; CountResidentLines counts them; KillResidents cleans them up.
// INTERNALS: stdio/listener session dispatch and deterministic query responses.
// @exports RunFakeResident, ResidentPS, CountResidentLines, KillResidents
// @deps Go standard library JSON, network, process, synchronization and testing APIs
package testfixture

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// -- 1 CORE · RunFakeResident <- START HERE --
func RunFakeResident() int {
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
	if err := os.Chmod(socket, 0o600); err != nil {
		return 1
	}
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
			defer mu.Unlock()
			if clients == 0 {
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
				if clients == 0 {
					arm()
				}
				mu.Unlock()
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
			"result": map[string]any{
				"hit": request.Query, "k": request.K,
				"databases": []string{"ops"}, "vector_executed": true,
				"results": []map[string]any{{
					"rank": 1, "score": 0.5, "database": "ops", "table": "memories",
					"id": "1", "source": "memories", "source_id": "1", "text": request.Query,
				}},
			},
		})
	}
}

// -/ 1

// -- 2 HELPER · Process observation and cleanup --
func ResidentPS(t *testing.T, hint string) string {
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

func CountResidentLines(psOutput string) int {
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

func KillResidents(hint string) {
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

// -/ 2
