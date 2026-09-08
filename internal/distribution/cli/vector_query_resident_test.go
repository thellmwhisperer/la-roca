package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestParseVectorQueryInvocation(t *testing.T) {
	inv, ok := parseVectorQueryInvocation([]string{
		"--json", "query", "--expand-templates", "--min-score", "0.35",
		"--databases", "ops", "harbor lantern", "3",
	})
	if !ok {
		t.Fatal("query invocation was not recognized")
	}
	if inv.query != "harbor lantern" || inv.k != 3 || inv.databases != "ops" ||
		!inv.json || !inv.expandTemplates || inv.minScore != 0.35 {
		t.Fatalf("parsed invocation = %+v", inv)
	}
	if _, ok := parseVectorQueryInvocation([]string{"status"}); ok {
		t.Fatal("status was parsed as a query")
	}
}

func TestParseVectorQueryInvocationCompatibility(t *testing.T) {
	for _, args := range [][]string{
		{"query", "harbor", "--json=false", "--expand-templates=false", "--min-score=0.8"},
		{"--json", "query", "harbor", "--json=false", "--expand-templates=true", "--expand-templates=false", "--min-score=0.8"},
	} {
		inv, ok := parseVectorQueryInvocation(args)
		if !ok || inv.json || inv.expandTemplates || inv.minScore != 0.8 {
			t.Fatalf("parse %v = %+v, %v", args, inv, ok)
		}
	}
	inv, ok := parseVectorQueryInvocation([]string{"query", "--", "--harbor", "3"})
	if !ok || inv.query != "--harbor" || inv.k != 3 {
		t.Fatalf("literal query = %+v, %v", inv, ok)
	}
	for _, args := range [][]string{
		{"query", "harbor", "--unknown"},
		{"query", "harbor", "--help"},
		{"query", "harbor", "-h"},
		{"query", "harbor", "3", "extra"},
		{"query", "harbor", "--json=invalid"},
		{"query", "harbor", "--expand-templates=invalid"},
		{"query", "harbor", "--db-path"},
		{"query", "harbor", "--min-score=invalid"},
		{"query", "harbor", "--state-dir=custom"},
		{"query", "harbor", "--progress-fd=3"},
		{"--", "query", "harbor"},
	} {
		if inv, ok := parseVectorQueryInvocation(args); ok {
			t.Fatalf("unsupported invocation %v intercepted as %+v", args, inv)
		}
	}
}

func TestVectorQueryResidentOptions(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	t.Setenv("ROCA_VECTOR_PLUGIN_ROOT", "")
	t.Setenv("ROCA_VECTOR_STATE_DIR", "")
	paths, err := config.Resolve(config.Input{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	for _, db := range []string{"./roca.db", "nested/roca.db", paths.DB} {
		opts, err := vectorQueryResidentOptions(vectorQueryInvocation{dbPath: db}, "companion", paths)
		if err != nil {
			t.Fatal(err)
		}
		absolute, err := filepath.Abs(db)
		if err != nil {
			t.Fatal(err)
		}
		if opts.DBPath != absolute || opts.DataDir != filepath.Dir(absolute) {
			t.Fatalf("database %q resolved to %+v", db, opts)
		}
		if opts.PluginRoot != pluginRoot(paths) || opts.StateDir != filepath.Join(pluginRoot(paths), "roca-vector", "state") {
			t.Fatalf("default directories = %+v", opts)
		}
		host, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		if opts.HostBinary != host || opts.Binary != "companion" {
			t.Fatalf("executables = %+v", opts)
		}
	}
	t.Setenv("ROCA_VECTOR_PLUGIN_ROOT", "alternate-plugins")
	t.Setenv("ROCA_VECTOR_STATE_DIR", "alternate-state")
	opts, err := vectorQueryResidentOptions(vectorQueryInvocation{}, "companion", paths)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.Abs("alternate-plugins")
	state, _ := filepath.Abs("alternate-state")
	if opts.PluginRoot != root || opts.StateDir != state || opts.DBPath != paths.DB {
		t.Fatalf("directory overrides = %+v", opts)
	}
}

func TestVectorQueryResidentDelegatesUnavailable(t *testing.T) {
	for _, registry := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "startup failure"}[registry], func(t *testing.T) {
			home := t.TempDir()
			t.Chdir(home)
			t.Setenv("ROCA_VECTOR_PLUGIN_ROOT", home)
			t.Setenv("ROCA_VECTOR_STATE_DIR", home)
			t.Setenv("ROCA_VECTOR_RESIDENT_SOCKET", "socket/resident.sock")
			if registry {
				if err := os.WriteFile("vector-registry.json", []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			paths, err := config.Resolve(config.Input{Home: home})
			if err != nil {
				t.Fatal(err)
			}
			handled, code, err := runVectorQueryResident(&cliEnv{}, []string{"query", "harbor"}, filepath.Join(home, "missing-companion"), paths)
			if handled || code != 0 || err != nil {
				t.Fatalf("fallback = %v, %d, %v", handled, code, err)
			}
			if !registry {
				if _, err := os.Stat("socket"); !os.IsNotExist(err) {
					t.Fatalf("standalone query attempted resident startup: %v", err)
				}
			}
		})
	}
}

func TestVectorQueryResidentPreservesQueryOutcome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix resident transport")
	}
	for _, queryError := range []string{"", "sidecar unreadable"} {
		t.Run("query "+queryError, func(t *testing.T) {
			home := t.TempDir()
			if err := os.Chmod(home, 0700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(home)
			t.Setenv("ROCA_VECTOR_PLUGIN_ROOT", home)
			t.Setenv("ROCA_VECTOR_STATE_DIR", home)
			t.Setenv("ROCA_VECTOR_RESIDENT_SOCKET", "resident.sock")
			if err := os.WriteFile("vector-registry.json", []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: "resident.sock", Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := os.Chmod("resident.sock", 0600); err != nil {
				t.Fatal(err)
			}
			if err := listener.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				encoder := json.NewEncoder(conn)
				_ = encoder.Encode(map[string]any{"kind": "result", "stage": "prewarm", "extra": map[string]any{"query_options": []string{"expand_templates", "min_score"}}})
				var request struct {
					ID     int64
					Query  string
					K      int
					Expand bool `json:"expand_templates"`
				}
				if err := json.NewDecoder(conn).Decode(&request); err != nil {
					done <- err
					return
				}
				if request.Query != "harbor" || request.K != 3 || request.Expand {
					done <- fmt.Errorf("unexpected request: %+v", request)
					return
				}
				response := map[string]any{"kind": "result", "stage": "query", "id": request.ID, "result": map[string]any{"results": []any{}, "vector_executed": true}}
				if queryError != "" {
					response["kind"] = "error"
					response["error"] = queryError
				}
				if err := encoder.Encode(response); err != nil {
					done <- err
					return
				}
				_, err = io.Copy(io.Discard, conn)
				done <- err
			}()
			paths, err := config.Resolve(config.Input{Home: home})
			if err != nil {
				t.Fatal(err)
			}
			output := new(bytes.Buffer)
			env := &cliEnv{out: output, errOut: io.Discard}
			handled, code, err := runVectorQueryResident(env, []string{"query", "harbor", "3", "--json", "--expand-templates=false"}, "missing-companion", paths)
			if !handled {
				t.Fatal("query outcome was delegated")
			}
			if queryError != "" {
				if code != ExitError || err == nil || !strings.Contains(err.Error(), queryError) {
					t.Fatalf("query failure = %d, %v", code, err)
				}
			} else {
				if code != ExitOK || err != nil {
					t.Fatalf("query success = %d, %v", code, err)
				}
				var result struct {
					VectorExecuted bool `json:"vector_executed"`
				}
				if err := json.Unmarshal(output.Bytes(), &result); err != nil || !result.VectorExecuted {
					t.Fatalf("output = %s, %v", output, err)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
