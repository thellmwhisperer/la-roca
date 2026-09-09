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
	"os/exec"
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

func TestClientReplyBeforeEOF(t *testing.T) {
	for _, test := range []struct {
		name  string
		reply string
		want  string
		err   string
	}{
		{"result", `{"kind":"result","stage":"query","id":1,"result":{"fresh":true}}` + "\n", `{"fresh":true}`, ""},
		{"query error", `{"kind":"error","stage":"query","id":1,"error":"query refused"}` + "\n", "", "query refused"},
		{"no reply", "", "", "unexpected EOF"},
		{"other query", `{"kind":"result","stage":"query","id":2,"result":{}}` + "\n", "", "unexpected EOF"},
	} {
		// Both select cases are ready; exercise either choice repeatedly.
		for attempt := range 20 {
			t.Run(fmt.Sprintf("%s/%d", test.name, attempt), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				clientEnd, serverEnd := net.Pipe()
				defer serverEnd.Close()
				conn := &replyBeforeWriteReturns{Conn: clientEnd, ctx: ctx}
				client := NewClient(conn, nil)
				defer client.Close()
				conn.decoded = client.failed
				go func() {
					defer serverEnd.Close()
					_, _ = fmt.Fprintln(serverEnd, `{"kind":"result","stage":"prewarm"}`)
					if _, err := bufio.NewReader(serverEnd).ReadBytes('\n'); err != nil {
						return
					}
					_, _ = io.WriteString(serverEnd, test.reply)
				}()
				if err := client.WaitReady(ctx); err != nil {
					t.Fatal(err)
				}
				raw, err := client.Query(ctx, Request{Query: "harbor lantern", K: 3})
				if test.err != "" {
					if err == nil || err.Error() != test.err {
						t.Fatalf("Query error = %v, want %q", err, test.err)
					}
				} else if err != nil || string(raw) != test.want {
					t.Fatalf("Query = %s, %v; want %s", raw, err, test.want)
				}
			})
		}
	}
}

// Let the decoder finish before Query starts waiting for its reply, as can
// happen when a resident sends an answer and immediately closes the connection.
type replyBeforeWriteReturns struct {
	net.Conn
	ctx     context.Context
	decoded <-chan struct{}
}

func (c *replyBeforeWriteReturns) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	select {
	case <-c.decoded:
	case <-c.ctx.Done():
	}
	return n, err
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
	var legacyConnections, currentConnections atomic.Int32
	listenTestResident(t, primary, nil, &legacyConnections, `{"legacy":true}`)
	listenTestResident(t, primary+".current", map[string]any{
		"query_options": []any{"expand_templates", "min_score"},
	}, &currentConnections, `{"current":true}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := QueryOnce(ctx, Options{DataDir: dir}, Request{Query: "harbor lantern", K: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"current":true`)) {
		t.Fatalf("result = %s", raw)
	}
	if currentConnections.Load() != 1 {
		t.Fatalf("current connections = %d, want 1", currentConnections.Load())
	}
	if legacyConnections.Load() != 0 {
		t.Fatalf("legacy connections = %d, want 0", legacyConnections.Load())
	}
}

func listenTestResident(t *testing.T, socket string, extra map[string]any, connections *atomic.Int32, result string) {
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
			connections.Add(1)
			go func(conn net.Conn) {
				defer conn.Close()
				encoder := json.NewEncoder(conn)
				_ = encoder.Encode(envelope{Kind: "result", Stage: "prewarm", Extra: extra})
				var request map[string]any
				if err := json.NewDecoder(conn).Decode(&request); err != nil {
					return
				}
				_ = encoder.Encode(map[string]any{
					"kind": "result", "stage": "query", "id": request["id"],
					"result": json.RawMessage(result),
				})
			}(conn)
		}
	}()
}

func TestSpawnEnvPreservesHostExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell environment probe")
	}
	for _, override := range []string{"", "/explicit/roca"} {
		t.Run("override="+override, func(t *testing.T) {
			t.Setenv("ROCA_VECTOR_ROCA_BINARY", override)
			command := exec.Command("/bin/sh", "-c", `printf '%s' "$ROCA_VECTOR_ROCA_BINARY"`)
			command.Env = append(os.Environ(), spawnEnv(Options{HostBinary: "/current/roca"})...)
			output, err := command.Output()
			if err != nil {
				t.Fatal(err)
			}
			want := override
			if want == "" {
				want = "/current/roca"
			}
			if string(output) != want {
				t.Fatalf("child host = %q, want %q", output, want)
			}
		})
	}
}
