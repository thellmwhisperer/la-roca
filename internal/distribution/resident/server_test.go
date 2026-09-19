package resident

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/mcpplug"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	transport "github.com/thellmwhisperer/la-roca/pkg/resident"
)

func TestResidentPreservesTheMCPHandshakeWhileSniffingTheProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix resident transport")
	}
	root := residentTestRoot(t)
	socket := filepath.Join(root, "resident.sock")
	t.Setenv("ROCA_RESIDENT_SOCKET", socket)
	svc, err := service.Open(service.Options{
		DBPath: filepath.Join(root, "roca.db"), DataDir: root,
		Version: "0.0.0-test", Commit: "0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, svc, mcpplug.Build{Version: "0.0.0-test", Commit: "0123456789abcdef"}) }()

	conn := waitResidentTestSocket(t, socket)
	defer conn.Close()
	initialize := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25", "capabilities": map[string]any{},
			"clientInfo": map[string]string{"name": "resident-test", "version": "1"},
		},
	}
	if err := json.NewEncoder(conn).Encode(initialize); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, `"serverInfo":{"name":"roca"`) {
		t.Fatalf("initialize response = %s", line)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resident did not stop after context cancellation")
	}
}

func TestResidentServesCLIStatusAlongsideMCPClients(t *testing.T) {
	root := residentTestRoot(t)
	socket := filepath.Join(root, "resident.sock")
	t.Setenv("ROCA_RESIDENT_SOCKET", socket)
	svc, err := service.Open(service.Options{DBPath: filepath.Join(root, "roca.db"), DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, svc, mcpplug.Build{}) }()
	conn := waitResidentTestSocket(t, socket)
	defer conn.Close()
	result, err := transport.Call(context.Background(), transport.Options{Socket: socket}, "status", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	var status transport.Status
	if err := json.Unmarshal(result, &status); err != nil {
		t.Fatal(err)
	}
	if status.PID == 0 || status.OpenConnections != 1 {
		t.Fatalf("status = %+v", status)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("resident did not stop")
	}
}

func waitResidentTestSocket(t *testing.T, socket string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socket, 100*time.Millisecond)
		if err == nil {
			return conn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("resident socket did not become ready: %s", socket)
	return nil
}

func residentTestRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "r439-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}
