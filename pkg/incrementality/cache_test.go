package incrementality_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/thellmwhisperer/la-roca/pkg/incrementality"
)

func TestCachedTargetFingerprintInvalidatesState(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("platform does not expose a file change identity")
	}
	path := filepath.Join(t.TempDir(), "memory.md")
	if err := os.WriteFile(path, []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := incrementality.Target{Path: path, ParserVersion: "v1"}
	first, err := incrementality.CachedTargetFingerprint(target, incrementality.FileState{})
	if err != nil {
		t.Fatal(err)
	}
	failed := incrementality.FileState{Fingerprint: "stale:" + first, LastError: "retry"}
	retried, err := incrementality.CachedTargetFingerprint(target, failed)
	if err != nil || retried != first {
		t.Fatalf("failed state was reused: fingerprint = %q, error = %v", retried, err)
	}
	known := incrementality.FileState{Fingerprint: first}
	for _, tc := range []struct {
		name     string
		machine  string
		version  string
		wantSame bool
	}{
		{"unchanged", "", "v1", true},
		{"parser revision", "", "v2", false},
		{"machine promotion", "hub", "v1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target.Machine, target.ParserVersion = tc.machine, tc.version
			got, err := incrementality.CachedTargetFingerprint(target, known)
			if err != nil || (got == first) != tc.wantSame {
				t.Fatalf("fingerprint = %q, error = %v, same wanted = %v", got, err, tc.wantSame)
			}
		})
	}
}
