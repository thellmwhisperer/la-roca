//go:build darwin

package model

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// The child runs under lab-only read instrumentation. No counters or test
// switches enter the model owner or any shipped query path.
func TestCostModelVerification(t *testing.T) {
	if os.Getenv("ROCA_D6_CHILD") == "1" {
		costModelVerification(t)
		return
	}
	lab, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := filepath.Abs("../../../../testdata/d6-model/reads-darwin.c")
	if err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(lab, "reads.dylib")
	if out, err := exec.Command("clang", "-dynamiclib", "-o", library, source).CombinedOutput(); err != nil {
		t.Fatalf("instrumentation: %v: %s", err, out)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestCostModelVerification$", "-test.v")
	cmd.Env = append(os.Environ(), "ROCA_D6_CHILD=1", "ROCA_D6_LAB="+lab,
		"ROCA_D6_COUNTER="+filepath.Join(lab, "counter"), "DYLD_INSERT_LIBRARIES="+library,
		"ROCA_D6_MODEL="+FilePath(lab, costManifest()))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cost fixture: %v: %s", err, out)
	} else {
		t.Logf("%s", out)
	}
}

func costManifest() Manifest {
	payload := []byte("synthetic verified model")
	sum := sha256.Sum256(payload)
	return Manifest{ID: "fixture", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(payload)), URL: "https://example.invalid/model"}
}

func costModelVerification(t *testing.T) {
	root := os.Getenv("ROCA_D6_LAB")
	payload := []byte("synthetic verified model")
	manifest := costManifest()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
	defer server.Close()
	manifest.URL = server.URL
	path := FilePath(root, manifest)
	count := func() uint64 {
		b, err := os.ReadFile(os.Getenv("ROCA_D6_COUNTER") + "." + strconv.Itoa(os.Getpid()))
		if err != nil || len(b) != 8 {
			t.Fatalf("counter unavailable: %v (%d bytes)", err, len(b))
		}
		return binary.LittleEndian.Uint64(b)
	}
	var previous uint64
	check := func(stage string, want uint64) {
		now := count()
		if now-previous != want {
			t.Fatalf("%s: model bytes=%d want=%d", stage, now-previous, want)
		}
		previous = now
		t.Logf("%s: bytes=%d, verified path unchanged", stage, want)
	}
	if _, err := Ensure(context.Background(), root, manifest, nil); err != nil {
		t.Fatal(err)
	}
	check("download", uint64(len(payload)))
	for range 2 {
		if got, err := Existing(root, manifest); err != nil || got != path {
			t.Fatalf("existing: %s %v", got, err)
		}
		if got, err := Ensure(context.Background(), root, manifest, nil); err != nil || got != path {
			t.Fatalf("ensure: %s %v", got, err)
		}
	}
	check("unchanged setup and open", 0)
	if err := os.WriteFile(path+".replacement", payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".replacement", path); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got, err := Existing(root, manifest); err != nil || got != path {
			t.Fatalf("replace existing: %s %v", got, err)
		}
		if got, err := Ensure(context.Background(), root, manifest, nil); err != nil || got != path {
			t.Fatalf("replace ensure: %s %v", got, err)
		}
	}
	check("replacement setup and open", uint64(len(payload)))
}
