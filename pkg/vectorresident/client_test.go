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
