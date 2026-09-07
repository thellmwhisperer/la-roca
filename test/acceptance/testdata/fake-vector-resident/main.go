package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

func main() {
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
		serve(os.Stdin, os.Stdout)
		return
	}
	os.Exit(listenAndServe(listen, idle))
}

func listenAndServe(socket string, idle time.Duration) int {
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
			serve(conn, conn)
		}()
	}
}

func serve(in io.Reader, out io.Writer) {
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
		}
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			_ = encoder.Encode(map[string]any{"kind": "error", "stage": "query", "error": err.Error()})
			continue
		}
		_ = encoder.Encode(map[string]any{
			"kind": "result", "stage": "query", "id": request.ID,
			"result": map[string]any{"hit": request.Query},
		})
	}
}
