package vectorresident

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientRoutesByIDAndStreamsProductStatus(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	t.Cleanup(func() {
		_ = clientEnd.Close()
		_ = serverEnd.Close()
	})
	status := new(bytes.Buffer)
	client := NewClient(clientEnd, status)
	go func() {
		_, _ = fmt.Fprint(serverEnd, ""+
			`{"kind":"progress","stage":"prewarm","message":"semantic search: preparing"}`+"\n"+
			`{"kind":"result","stage":"prewarm","message":"semantic search: ready"}`+"\n")
		line, err := bufio.NewReader(serverEnd).ReadBytes('\n')
		if err != nil {
			return
		}
		var request map[string]any
		if err := json.Unmarshal(line, &request); err != nil {
			return
		}
		_, _ = fmt.Fprintf(serverEnd, `{"kind":"result","stage":"query","id":%v,"result":{"fresh":true}}`+"\n", request["id"])
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := client.Query(ctx, Request{Query: "harbor lantern", K: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"fresh":true`)) {
		t.Fatalf("result = %s", raw)
	}
	if status.String() != "semantic search: preparing\n" {
		t.Fatalf("status = %q", status.String())
	}
}

func TestClientCleanEOFBeforeReadyWakesWaiters(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	_ = serverEnd.Close()
	client := NewClient(clientEnd, nil)
	t.Cleanup(func() { _ = client.Close() })
	err := client.WaitReady(context.Background())
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("WaitReady error = %v, want unexpected EOF", err)
	}
}

func TestQueryOnceUnavailableWithoutBinaryOrListener(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rv-u-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ROCA_VECTOR_RESIDENT_SOCKET", "")
	_, err = QueryOnce(context.Background(), Options{DataDir: dir}, Request{Query: "harbor lantern", K: 3})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("QueryOnce = %v, want unavailable", err)
	}
}

func TestClientRequiresAdvertisedQueryOptions(t *testing.T) {
	for _, test := range []struct {
		name        string
		extra       map[string]any
		request     Request
		unsupported bool
	}{
		{"legacy plain", nil, Request{Query: "harbor"}, false},
		{"legacy expanded", nil, Request{Query: "harbor", ExpandTemplates: true}, true},
		{"legacy score", nil, Request{Query: "harbor", MinScore: 0.35}, true},
		{"partial support", map[string]any{"query_options": []string{"expand_templates"}}, Request{Query: "harbor", ExpandTemplates: true, MinScore: 0.35}, true},
		{"supported", map[string]any{"query_options": []string{"expand_templates", "min_score"}}, Request{Query: "harbor", ExpandTemplates: true, MinScore: 0.35}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			clientEnd, serverEnd := net.Pipe()
			defer serverEnd.Close()
			client := NewClient(clientEnd, nil)
			defer client.Close()
			seen := make(chan map[string]any, 1)
			go func() {
				encoder := json.NewEncoder(serverEnd)
				_ = encoder.Encode(envelope{Kind: "result", Stage: "prewarm", Extra: test.extra})
				var request map[string]any
				if err := json.NewDecoder(serverEnd).Decode(&request); err == nil {
					seen <- request
					_ = encoder.Encode(map[string]any{"kind": "result", "stage": "query", "id": request["id"], "result": map[string]any{"results": []any{}}})
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := client.WaitReady(ctx); err != nil {
				t.Fatal(err)
			}
			_, err := client.Query(ctx, test.request)
			if test.unsupported {
				if !errors.Is(err, ErrUnsupportedOptions) {
					t.Fatalf("query error = %v", err)
				}
				select {
				case request := <-seen:
					t.Fatalf("unsupported query sent: %v", request)
				default:
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				request := <-seen
				if test.request.ExpandTemplates && (request["expand_templates"] != true || request["min_score"] != test.request.MinScore) {
					t.Fatalf("query options = %v", request)
				}
			}
		})
	}
}

func TestQueryOnceSkipsLegacyResident(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are the shared resident transport")
	}
	dir, err := os.MkdirTemp("/tmp", "rv-c-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCA_VECTOR_RESIDENT_SOCKET", "")
	primary, _ := SocketPaths(dir)
	if err := os.MkdirAll(filepath.Dir(primary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(primary), 0o700); err != nil {
		t.Fatal(err)
	}
	var legacyHits, currentHits atomic.Int32
	listenTestResident(t, primary, nil, &legacyHits, `{"legacy":true}`)
	listenTestResident(t, primary+".current", map[string]any{
		"query_options": []any{"expand_templates", "min_score"},
	}, &currentHits, `{"current":true}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := QueryOnce(ctx, Options{DataDir: dir}, Request{Query: "harbor lantern", K: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"current":true`)) {
		t.Fatalf("result = %s", raw)
	}
	if currentHits.Load() != 1 {
		t.Fatalf("current hits = %d, want 1", currentHits.Load())
	}
	if legacyHits.Load() != 0 {
		t.Fatalf("legacy hits = %d, want 0", legacyHits.Load())
	}
}

func listenTestResident(t *testing.T, socket string, extra map[string]any, hits *atomic.Int32, result string) {
	t.Helper()
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(socket)
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				encoder := json.NewEncoder(conn)
				_ = encoder.Encode(envelope{Kind: "result", Stage: "prewarm", Extra: extra})
				var request map[string]any
				if err := json.NewDecoder(conn).Decode(&request); err != nil {
					return
				}
				hits.Add(1)
				_ = encoder.Encode(map[string]any{
					"kind": "result", "stage": "query", "id": request["id"],
					"result": json.RawMessage(result),
				})
			}(conn)
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := Dial(socket)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("resident was not listening on %s: %v", socket, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
