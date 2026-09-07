package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestListenResidentSocketLeavesALiveResidentAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are the shared resident transport")
	}
	socket := shortSocket(t)
	listener, err := listenResidentSocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, err = listenResidentSocket(socket)
	if !errors.Is(err, errResidentAlreadyRunning) {
		t.Fatalf("second listen = %v, want already running", err)
	}
}

func TestListenResidentSocketReplacesAStaleSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are the shared resident transport")
	}
	socket := shortSocket(t)
	if err := os.WriteFile(socket, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := listenResidentSocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatalf("dial after replacing stale socket: %v", err)
	}
	conn.Close()
}

func TestListeningResidentExitsAfterIdleWithNoClients(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are the shared resident transport")
	}
	listener := boundResident(t)
	done := make(chan error, 1)
	go func() {
		done <- serveListeningResident(context.Background(), listener, 80*time.Millisecond, immediateResidentSession())
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("resident did not exit after idle")
	}
}

func TestListeningResidentKeepsALiveClientPastIdle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are the shared resident transport")
	}
	listener := boundResident(t)
	done := make(chan error, 1)
	go func() {
		done <- serveListeningResident(context.Background(), listener, 80*time.Millisecond, immediateResidentSession())
	}()
	conn := dialResident(t, listener.Addr().String())
	defer conn.Close()
	awaitReady(t, conn)
	select {
	case err := <-done:
		t.Fatalf("resident exited while a client was attached: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	conn.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("resident did not exit after the last client disconnected")
	}
}

func TestListeningResidentAnswersTwoClients(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are the shared resident transport")
	}
	listener := boundResident(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = serveListeningResident(ctx, listener, time.Minute, immediateResidentSession())
	}()
	var seen sync.WaitGroup
	seen.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer seen.Done()
			conn := dialResident(t, listener.Addr().String())
			defer conn.Close()
			awaitReady(t, conn)
			if _, err := io.WriteString(conn, `{"id":7,"op":"query","query":"harbor lantern","k":3}`+"\n"); err != nil {
				t.Error(err)
				return
			}
			line, err := bufio.NewReader(conn).ReadBytes('\n')
			if err != nil {
				t.Error(err)
				return
			}
			var envelope struct {
				Kind   string `json:"kind"`
				Result struct {
					Hit string `json:"hit"`
				} `json:"result"`
			}
			if err := json.Unmarshal(line, &envelope); err != nil {
				t.Error(err)
				return
			}
			if envelope.Kind != "result" || envelope.Result.Hit != "harbor lantern" {
				t.Errorf("query envelope = %s", line)
			}
		}()
	}
	seen.Wait()
}

func boundResident(t *testing.T) net.Listener {
	t.Helper()
	listener, err := listenResidentSocket(shortSocket(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

func shortSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rv-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "resident.sock")
}

func immediateResidentSession() residentSession {
	return residentSession{
		waitReady: func(context.Context) error { return nil },
		extra:     map[string]any{"prewarm_ms": 1},
		query: func(_ context.Context, request residentRequest) (any, error) {
			return map[string]string{"hit": request.Query}, nil
		},
	}
}

func dialResident(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", addr, 50*time.Millisecond)
		if err == nil {
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial resident: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func awaitReady(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var envelope struct {
			Kind  string `json:"kind"`
			Stage string `json:"stage"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Stage == "prewarm" && (envelope.Kind == "result" || envelope.Kind == "error") {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("resident never became ready")
}

func TestListeningResidentRetiresAfterTerminalQueryFailure(t *testing.T) {
	listener := boundResident(t)
	session := immediateResidentSession()
	session.query = func(context.Context, residentRequest) (any, error) {
		return nil, errResidentUnusable
	}
	done := make(chan error, 1)
	go func() {
		done <- serveListeningResident(context.Background(), listener, time.Hour, session)
	}()
	peer := dialResident(t, listener.Addr().String())
	defer peer.Close()
	awaitReady(t, peer)
	conn := dialResident(t, listener.Addr().String())
	defer conn.Close()
	decoder := json.NewDecoder(conn)
	var event map[string]any
	for i := 0; i < 2; i++ {
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
	}
	if err := json.NewEncoder(conn).Encode(residentRequest{ID: 1, Query: "harbor"}); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&event); err != nil || event["kind"] != "error" {
		t.Fatalf("terminal response = %v, %v", event, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unusable resident stayed alive with clients attached")
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("peer was not disconnected: %v", err)
	}
	replacement, err := listenResidentSocket(listener.Addr().String())
	if err != nil {
		t.Fatalf("replacement resident could not start: %v", err)
	}
	defer replacement.Close()
}

func TestListeningResidentRetiresAfterPrewarmFailure(t *testing.T) {
	listener := boundResident(t)
	session := immediateResidentSession()
	session.waitReady = func(context.Context) error { return errors.New("model absent") }
	done := make(chan error, 1)
	go func() {
		done <- serveListeningResident(context.Background(), listener, time.Hour, session)
	}()
	conn := dialResident(t, listener.Addr().String())
	defer conn.Close()
	awaitReady(t, conn)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("prewarm failure kept the resident alive")
	}
}
