package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
)

func TestQueryUsesAListeningResidentWithoutLoadingTheEmbedder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are the shared resident transport")
	}
	listener := boundResident(t)
	socket := listener.Addr().String()
	var seen atomic.Int32
	session := residentSession{
		waitReady: func(context.Context) error { return nil },
		query: func(_ context.Context, request residentRequest) (any, error) {
			seen.Add(1)
			if request.Query != "harbor lantern" || request.K != 3 || request.Databases != "ops" {
				t.Errorf("resident request = %+v", request)
			}
			if !request.ExpandTemplates || request.MinScore != 0.35 {
				t.Errorf("expand flags = %+v", request)
			}
			return vector.FederatedQuery{
				Databases:      []string{"ops"},
				VectorExecuted: true,
				Results: []vector.Result{{
					Rank: 1, Score: 0.91, Database: "ops", Table: "memories",
					ID: "mem-1", Source: "memories", SourceID: "mem-1",
					Text: "harbor lantern",
				}},
			}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = serveListeningResident(ctx, listener, time.Minute, session) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", socket, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resident was not listening: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_VECTOR_PLUGIN_ROOT", "")
	t.Setenv("ROCA_VECTOR_RESIDENT_SOCKET", socket)
	t.Setenv("ROCA_VECTOR_RESIDENT_BINARY", "")
	var embeds atomic.Int32
	oldEmbedder := newEmbedder
	t.Cleanup(func() { newEmbedder = oldEmbedder })
	newEmbedder = func(*environment) vector.Embedder {
		embeds.Add(1)
		return stubEmbedder{}
	}
	oldLaunch := launchWorker
	t.Cleanup(func() { launchWorker = oldLaunch })
	launchWorker = func(vector.LaunchRequest) (vector.LaunchResult, error) {
		return vector.LaunchResult{}, nil
	}
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	env := &environment{dbPath: filepath.Join(home, "roca.db"), json: true, stateDir: state}
	output := executeForOutput(t, env, "--json", "query", "--expand-templates",
		"--min-score", "0.35", "--databases", "ops", "harbor lantern", "3")
	if embeds.Load() != 0 {
		t.Fatalf("query constructed the in-process embedder %d times", embeds.Load())
	}
	if seen.Load() != 1 {
		t.Fatalf("resident queries = %d, want 1", seen.Load())
	}
	var envelope struct {
		VectorExecuted bool `json:"vector_executed"`
		Results        []struct {
			Text string `json:"text"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.VectorExecuted || len(envelope.Results) != 1 || envelope.Results[0].Text != "harbor lantern" {
		t.Fatalf("decoded output = %s", output)
	}
}

func TestQueryReplacesAStaleResidentSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are the shared resident transport")
	}
	dir, err := os.MkdirTemp("/tmp", "rv-stale-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "resident.sock")
	if err := os.WriteFile(socket, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "roca-vector")
	body := "#!/bin/sh\n" +
		"socket=\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in --listen) shift; socket=$1 ;; *) shift ;; esac\n" +
		"done\n" +
		"python3 - \"$socket\" <<'PY'\n" +
		"import json, os, socket, sys\n" +
		"path = sys.argv[1]\n" +
		"try: os.unlink(path)\n" +
		"except FileNotFoundError: pass\n" +
		"server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)\n" +
		"server.bind(path)\n" +
		"os.chmod(path, 0o600)\n" +
		"server.listen(4)\n" +
		"conn, _ = server.accept()\n" +
		"conn.sendall(b'{\"kind\":\"result\",\"stage\":\"prewarm\",\"message\":\"semantic search: ready\"}\\n')\n" +
		"line = b''\n" +
		"while not line.endswith(b'\\n'):\n" +
		"    chunk = conn.recv(4096)\n" +
		"    if not chunk: break\n" +
		"    line += chunk\n" +
		"req = json.loads(line.decode())\n" +
		"result = {'databases':['ops'],'vector_executed':True,'results':[{'rank':1,'score':0.5,'database':'ops','table':'memories','id':'1','source':'memories','source_id':'1','text':'replaced'}]}\n" +
		"conn.sendall((json.dumps({'kind':'result','stage':'query','id':req.get('id',0),'result':result})+'\\n').encode())\n" +
		"conn.close()\n" +
		"PY\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_VECTOR_RESIDENT_SOCKET", socket)
	t.Setenv("ROCA_VECTOR_RESIDENT_BINARY", script)
	oldLaunch := launchWorker
	t.Cleanup(func() { launchWorker = oldLaunch })
	launchWorker = func(vector.LaunchRequest) (vector.LaunchResult, error) {
		return vector.LaunchResult{}, nil
	}
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	env := &environment{dbPath: filepath.Join(home, "roca.db"), json: true, stateDir: state}
	output := executeForOutput(t, env, "--json", "query", "harbor lantern", "3")
	if !strings.Contains(output, "replaced") {
		t.Fatalf("stale socket query output = %s", output)
	}
}
