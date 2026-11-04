package resident

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func proxyListener(t *testing.T) (net.Listener, Options) {
	t.Helper()
	dir, err := os.MkdirTemp("", "r439-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "test.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener, Options{Socket: socket}
}

func acceptProxy(listener net.Listener) (net.Conn, *bufio.Reader, error) {
	conn, err := listener.Accept()
	if err != nil {
		return nil, nil, err
	}
	reader := bufio.NewReader(conn)
	if _, err := reader.ReadBytes('\n'); err != nil {
		conn.Close()
		return nil, nil, err
	}
	if _, err := fmt.Fprintln(conn, `{"result":{}}`); err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, reader, nil
}

func TestProxyForwardsCancellationAndEOFWhileRequestIsPending(t *testing.T) {
	listener, options := proxyListener(t)
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ProxyStdio(ctx, options, input, io.Discard) }()
	observed := make(chan error, 1)
	go func() {
		conn, reader, err := acceptProxy(listener)
		if err != nil {
			observed <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := reader.ReadBytes('\n'); err != nil {
			observed <- err
			return
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			observed <- err
			return
		}
		var message struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(line, &message); err != nil {
			observed <- err
			return
		}
		if message.Method != "notifications/cancelled" {
			observed <- fmt.Errorf("method=%s", message.Method)
			return
		}
		observed <- nil
		_, err = reader.ReadBytes('\n')
		if !errors.Is(err, io.EOF) {
			observed <- fmt.Errorf("disconnect=%v", err)
			return
		}
		observed <- nil
	}()
	go func() {
		fmt.Fprintln(writer, `{"jsonrpc":"2.0","id":1,"method":"tools/call"}`)
		fmt.Fprintln(writer, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	}()
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
	writer.Close()
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProxyReconnectsWithoutReplayingAnUncertainWrite(t *testing.T) {
	listener, options := proxyListener(t)
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	output, sink := io.Pipe()
	defer output.Close()
	defer sink.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ProxyStdio(ctx, options, input, sink) }()
	observed := make(chan error, 1)
	go func() {
		for attempt := 0; attempt < 2; attempt++ {
			conn, reader, err := acceptProxy(listener)
			if err != nil {
				observed <- err
				return
			}
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			line, err := reader.ReadBytes('\n')
			if err != nil {
				conn.Close()
				observed <- err
				return
			}
			var request struct {
				Method string `json:"method"`
				ID     int    `json:"id"`
			}
			if err := json.Unmarshal(line, &request); err != nil || request.Method != "initialize" {
				conn.Close()
				observed <- fmt.Errorf("initialize=%s err=%v", line, err)
				return
			}
			fmt.Fprintln(conn, `{"jsonrpc":"2.0","id":1,"result":{}}`)
			if _, err := reader.ReadBytes('\n'); err != nil {
				conn.Close()
				observed <- err
				return
			}
			line, err = reader.ReadBytes('\n')
			if err != nil {
				conn.Close()
				observed <- err
				return
			}
			if err := json.Unmarshal(line, &request); err != nil || request.ID != attempt+2 {
				conn.Close()
				observed <- fmt.Errorf("request=%s err=%v", line, err)
				return
			}
			if attempt == 1 {
				fmt.Fprintln(conn, `{"jsonrpc":"2.0","id":3,"result":{}}`)
			}
			conn.Close()
		}
		observed <- nil
	}()
	decoder := json.NewDecoder(output)
	read := func() map[string]json.RawMessage {
		t.Helper()
		var value map[string]json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	fmt.Fprintln(writer, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	read()
	fmt.Fprintln(writer, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	fmt.Fprintln(writer, `{"jsonrpc":"2.0","id":2,"method":"tools/call"}`)
	result := read()
	if string(result["id"]) != "2" || !strings.Contains(string(result["error"]), "request outcome is unknown") {
		t.Fatalf("uncertain request=%v", result)
	}
	fmt.Fprintln(writer, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	result = read()
	if string(result["id"]) != "3" || len(result["result"]) == 0 {
		t.Fatalf("reconnected request=%v", result)
	}
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
	writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProxyResendsARequestARetiredResidentRefused(t *testing.T) {
	listener, options := proxyListener(t)
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	output, sink := io.Pipe()
	defer output.Close()
	defer sink.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ProxyStdio(ctx, options, input, sink) }()
	observed := make(chan error, 1)
	go func() {
		// The first resident retires on the call; the second runs it.
		for attempt := 0; attempt < 2; attempt++ {
			conn, reader, err := acceptProxy(listener)
			if err != nil {
				observed <- err
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			for _, want := range []string{"initialize", "notifications/initialized", "tools/call"} {
				line, err := reader.ReadBytes('\n')
				var request struct {
					Method string `json:"method"`
				}
				if err != nil || json.Unmarshal(line, &request) != nil || request.Method != want {
					observed <- fmt.Errorf("attempt %d: want %s, got %s err=%v", attempt, want, line, err)
					return
				}
				if want == "initialize" {
					fmt.Fprintln(conn, `{"jsonrpc":"2.0","id":1,"result":{}}`)
				}
			}
			if attempt == 0 {
				fmt.Fprintf(conn, `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":%q}}`+"\n", ErrRetired.Error())
			} else {
				fmt.Fprintln(conn, `{"jsonrpc":"2.0","id":2,"result":{"answered":"new"}}`)
			}
		}
		observed <- nil
	}()
	decoder := json.NewDecoder(output)
	var value map[string]json.RawMessage
	fmt.Fprintln(writer, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(writer, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	fmt.Fprintln(writer, `{"jsonrpc":"2.0","id":2,"method":"tools/call"}`)
	value = nil
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	if string(value["id"]) != "2" || string(value["result"]) != `{"answered":"new"}` {
		t.Fatalf("refused request = %v", value)
	}
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
	writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProxyUnavailableStartupReturnsWithoutRetryingForever(t *testing.T) {
	_, options := proxyListener(t)
	options.Socket += ".missing"
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go fmt.Fprintln(writer, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	started := time.Now()
	err := ProxyStdio(ctx, options, input, io.Discard)
	if !errors.Is(err, ErrUnavailable) || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("startup=%v duration=%s", err, time.Since(started))
	}
}

func TestDialRefusesASocketWithGroupOrOtherBits(t *testing.T) {
	_, opts := proxyListener(t)
	if err := os.Chmod(opts.Socket, 0o660); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(opts); err == nil || err.Error() != "unsafe resident socket: "+opts.Socket {
		t.Fatalf("dial = %v", err)
	}
}
