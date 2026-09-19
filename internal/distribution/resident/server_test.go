package resident

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/pkg/vectorresident"
	"io"
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

func startReviewResident(t *testing.T) (*service.Service, transport.Options) {
	t.Helper()
	root := residentTestRoot(t)
	socket := filepath.Join(root, "resident.sock")
	t.Setenv("ROCA_RESIDENT_SOCKET", socket)
	svc, err := service.Open(service.Options{DBPath: filepath.Join(root, "roca.db"), DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, svc, mcpplug.Build{}) }()
	waitResidentTestSocket(t, socket).Close()
	t.Cleanup(func() { cancel(); <-done; svc.Close() })
	return svc, transport.Options{Socket: socket, DBPath: svc.ConfiguredDBPath()}
}

func TestResidentRejectsAnotherDatabaseBeforeDispatch(t *testing.T) {
	svc, options := startReviewResident(t)
	options.DBPath = filepath.Join(svc.DataDir(), "other.db")
	_, err := transport.Call(context.Background(), options, "store", service.StoreRequest{Layer: "discovery", Content: "wrong database"})
	if !errors.Is(err, transport.ErrDatabaseMismatch) {
		t.Fatalf("mismatched database: %v", err)
	}
	_, err = transport.DialOrSpawn(context.Background(), options)
	if !errors.Is(err, transport.ErrDatabaseMismatch) {
		t.Fatalf("mismatched startup: %v", err)
	}
	var count int
	if err := svc.DB().SQL().QueryRow("SELECT COUNT(*) FROM memories").Scan(&count); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestResidentReadOnlySessionCannotWriteToWritableService(t *testing.T) {
	svc, options := startReviewResident(t)
	options.ReadOnly = true
	conn, err := transport.Dial(options)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "codex", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: conn, Writer: conn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "roca_store", Arguments: map[string]any{"layer": "discovery", "content": "must not write"}})
	if err != nil || !result.IsError {
		t.Fatalf("store = %+v err=%v", result, err)
	}
	body, _ := json.Marshal(result)
	if !strings.Contains(string(body), "read-only") {
		t.Fatalf("wrong refusal: %s", body)
	}
	var count int
	if err := svc.DB().SQL().QueryRow("SELECT COUNT(*) FROM memories").Scan(&count); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	options.ReadOnly = false
	if _, err := transport.Call(ctx, options, "store", service.StoreRequest{Layer: "discovery", Content: "writable session survives"}); err != nil {
		t.Fatal(err)
	}
}

func TestResidentStorePreservesNumericMetadata(t *testing.T) {
	svc, options := startReviewResident(t)
	_, err := transport.Call(context.Background(), options, "store", service.StoreRequest{Layer: "discovery", Content: "precise number", Metadata: map[string]any{"number": json.Number("9007199254740993")}})
	if err != nil {
		t.Fatal(err)
	}
	var metadata string
	if err := svc.DB().SQL().QueryRow("SELECT metadata FROM memories WHERE content = 'precise number'").Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &values); err != nil {
		t.Fatal(err)
	}
	if string(values["number"]) != "9007199254740993" {
		t.Fatalf("metadata=%s", metadata)
	}
}

func TestResidentCountsPhysicalPoolConnections(t *testing.T) {
	svc, options := startReviewResident(t)
	read, err := svc.DB().ReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	first, err := read.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	baseline := svc.OpenConnections()
	second, err := read.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	payload, err := transport.Call(context.Background(), options, "status", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	var status transport.Status
	if err := json.Unmarshal(payload, &status); err != nil {
		t.Fatal(err)
	}
	if status.OpenConnections != baseline+1 {
		t.Fatalf("baseline=%d connections=%d", baseline, status.OpenConnections)
	}
}

func TestResidentDropsFailedVectorClient(t *testing.T) {
	root := residentTestRoot(t)
	svc, err := service.Open(service.Options{DBPath: filepath.Join(root, "roca.db"), VectorEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	clientSide, serverSide := net.Pipe()
	client := vectorresident.NewClient(clientSide, io.Discard)
	defer client.Close()
	go func() {
		fmt.Fprintln(serverSide, `{"kind":"result","stage":"prewarm"}`)
		bufio.NewReader(serverSide).ReadBytes('\n')
		serverSide.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	server := &Server{service: svc, vector: client}
	if _, err := server.vectorQuery(ctx, vectorresident.Request{Query: "test"}); err == nil {
		t.Fatal("crashed client returned success")
	}
	if server.vector != nil {
		t.Fatal("failed vector client remains cached")
	}
}
