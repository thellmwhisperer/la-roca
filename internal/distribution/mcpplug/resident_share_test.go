// @overview Verify shared resident connections, lifetime and socket ownership.
// READING GUIDE: TestThreeSessionsShareOneResident -> sharedResidentLab -> socket tests.
// MAIN FLOW: create isolated service -> start fake resident sessions -> assert sharing.
// PUBLIC API: TestMain dispatches fake resident invocations and the test suite.
// INTERNALS: sharedResidentLab, closeSharedResidentSession, labHint,
// privateResidentTestSocket, and isFakeResidentInvocation support the test cases.
// @exports TestMain, TestThreeSessionsShareOneResident,
// TestStaleResidentSocketDoesNotWedgeSpawn, TestResidentCloseDoesNotKillSharedProcess,
// TestSharedResidentExitsAfterIdleOnceClientsLeave, TestResidentRejectsPublicSocketDirectory,
// TestResidentRejectsSymlinkedSocket, TestResidentRejectsLongSocketPath
// @deps MCP SDK, provider service, testfixture, Go context/network/process APIs
package mcpplug

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"github.com/thellmwhisperer/la-roca/pkg/vectorresident"
	"github.com/thellmwhisperer/la-roca/test/testfixture"
)

// -- 1 HELPER · Fake resident dispatch --
func TestMain(m *testing.M) {
	if isFakeResidentInvocation() {
		os.Exit(testfixture.RunFakeResident())
	}
	os.Exit(m.Run())
}

func isFakeResidentInvocation() bool {
	for _, arg := range os.Args[1:] {
		if arg == "_resident" {
			return true
		}
	}
	return false
}

// -/ 1

// -- 2 CORE · Sharing and lifetime tests <- START HERE --
func TestThreeSessionsShareOneResident(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shared residency is proven on unix sockets")
	}
	svc := sharedResidentLab(t)
	var group sync.WaitGroup
	residents := make([]*residentVector, 3)
	errs := make([]error, 3)
	group.Add(3)
	for i := 0; i < 3; i++ {
		go func(i int) {
			defer group.Done()
			residents[i], errs[i] = startResidentVector(context.Background(), svc)
		}(i)
	}
	group.Wait()
	t.Cleanup(func() {
		for _, resident := range residents {
			if resident != nil {
				_ = resident.Close()
			}
		}
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
	}
	psOutput := testfixture.ResidentPS(t, labHint(svc))
	t.Logf("shared resident ps:\n%s", psOutput)
	if count := testfixture.CountResidentLines(psOutput); count != 1 {
		t.Fatalf("resident processes = %d, want 1\n%s", count, psOutput)
	}
	for i, resident := range residents {
		result, _, err := resident.call(context.Background(), nil, vectorQueryArgs{Query: "harbor lantern", K: 3})
		if err != nil {
			t.Fatalf("session %d query: %v", i, err)
		}
		if result == nil || len(result.Content) == 0 {
			t.Fatalf("session %d returned no content", i)
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok || !strings.Contains(text.Text, "harbor lantern") {
			t.Fatalf("session %d result = %#v", i, result.Content)
		}
	}
}

func TestStaleResidentSocketDoesNotWedgeSpawn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shared residency is proven on unix sockets")
	}
	svc := sharedResidentLab(t)
	socket, _ := residentSocketPaths(svc)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	resident, err := startResidentVector(context.Background(), svc)
	if err != nil {
		t.Fatalf("stale socket wedged spawn: %v", err)
	}
	t.Cleanup(func() { _ = resident.Close() })
	if err := resident.waitReady(context.Background()); err != nil {
		t.Fatalf("stale socket resident was not ready: %v", err)
	}
}

func TestResidentCloseDoesNotKillSharedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shared residency is proven on unix sockets")
	}
	svc := sharedResidentLab(t)
	closeSharedResidentSession(t, svc)
	second, err := startResidentVector(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	result, _, err := second.call(context.Background(), nil, vectorQueryArgs{Query: "harbor lantern"})
	if err != nil {
		t.Fatalf("query after first session closed: %v", err)
	}
	if result == nil {
		t.Fatal("query after first session closed returned nothing")
	}
	psOutput := testfixture.ResidentPS(t, labHint(svc))
	t.Logf("resident after first close:\n%s", psOutput)
	if count := testfixture.CountResidentLines(psOutput); count != 1 {
		t.Fatalf("resident processes = %d, want 1\n%s", count, psOutput)
	}
}

func TestSharedResidentExitsAfterIdleOnceClientsLeave(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shared residency is proven on unix sockets")
	}
	svc := sharedResidentLab(t)
	t.Setenv("ROCA_VECTOR_RESIDENT_IDLE", "200ms")
	closeSharedResidentSession(t, svc)
	deadline := time.Now().Add(2 * time.Second)
	for {
		psOutput := testfixture.ResidentPS(t, labHint(svc))
		if testfixture.CountResidentLines(psOutput) == 0 {
			t.Logf("resident gone after idle:\n%s", psOutput)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("resident still alive after idle\n%s", psOutput)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// -/ 2

// -- 3 HELPER · Service and socket fixtures --
func closeSharedResidentSession(t *testing.T, svc *service.Service) {
	t.Helper()
	first, err := startResidentVector(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.waitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
}

func sharedResidentLab(t *testing.T) *service.Service {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shared residency is proven on unix sockets")
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	data := filepath.Join(home, ".roca")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCA_VECTOR_RESIDENT_BINARY", exe)
	socket := privateResidentTestSocket(t)
	t.Setenv("ROCA_VECTOR_RESIDENT_SOCKET", socket)
	t.Setenv("ROCA_VECTOR_RESIDENT_IDLE", "2s")
	svc, err := service.Open(service.Options{
		DBPath: filepath.Join(data, "roca.db"), DataDir: data, VectorEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		svc.Close()
		testfixture.KillResidents(socket)
		_ = os.Remove(socket)
		_ = os.Remove(socket + ".lock")
	})
	return svc
}

func labHint(svc *service.Service) string {
	socket, _ := residentSocketPaths(svc)
	return socket
}

func privateResidentTestSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rv-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "resident.sock")
}

// -/ 3

// -- 4 CORE · Socket ownership tests --
func TestResidentRejectsPublicSocketDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket ownership")
	}
	socket := privateResidentTestSocket(t)
	if err := os.Chmod(filepath.Dir(socket), 0o777); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := dialOrSpawnResident(context.Background(), nil, "unused", socket, socket+".lock")
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("connected to a socket in a public directory")
	}
}

func TestResidentRejectsSymlinkedSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket ownership")
	}
	socket := privateResidentTestSocket(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := socket + ".alias"
	if err := os.Symlink(socket, alias); err != nil {
		t.Fatal(err)
	}
	conn, err := vectorresident.Dial(alias)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("connected through a symlinked endpoint")
	}
}

func TestResidentRejectsLongSocketPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket path limits")
	}
	socket := filepath.Join(t.TempDir(), strings.Repeat("x", 100), "resident.sock")
	t.Setenv("ROCA_VECTOR_RESIDENT_SOCKET", socket)
	selected, lock := residentSocketPaths(nil)
	conn, err := dialOrSpawnResident(context.Background(), nil, "unused", selected, lock)
	if conn != nil {
		conn.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("long socket path error = %v", err)
	}
}

// -/ 4
