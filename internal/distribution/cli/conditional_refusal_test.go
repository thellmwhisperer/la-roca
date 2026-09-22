package cli

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

func requireConditionalRefusal(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, securefile.ErrConditionalReplaceUnsupported) {
		t.Fatalf("error = %v, want conditional replacement refusal", err)
	}
}

func preserveFile(t *testing.T, path string) func() {
	t.Helper()
	before := string(mustRead(t, path))
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		current, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(mustRead(t, path)) != before || !os.SameFile(original, current) ||
			original.Mode() != current.Mode() {
			t.Fatalf("refusal changed bytes, identity, or permissions of %s", path)
		}
	}
}

func requireExactBackup(t *testing.T, path, previous string) {
	t.Helper()
	if string(mustRead(t, path)) != previous {
		t.Fatalf("backup %s did not preserve exact bytes", path)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup permissions: %v, err %v", info, err)
	}
}

func runRefusedHookCLI(t *testing.T, path string, args ...string) {
	t.Helper()
	preserved := preserveFile(t, path)
	previous := string(mustRead(t, path))
	root := rootCommand(&cliEnv{out: io.Discard, errOut: io.Discard, build: Build{Version: "v1.2.3"}})
	root.SetArgs(append([]string{"hooks"}, args...))
	requireConditionalRefusal(t, root.Execute())
	preserved()
	requireExactBackup(t, path+".roca.bak", previous)
}

func runRefusedSemanticConsent(t *testing.T, path, answer string) string {
	t.Helper()
	t.Setenv("CI", "")
	preserved := preserveFile(t, path)
	var out strings.Builder
	env := &cliEnv{out: &out, errOut: &out}
	err := env.offerSemanticSearch(t.Context(), bufio.NewReader(strings.NewReader(answer)),
		true, config.Paths{Home: filepath.Dir(path), Config: path}, true, readyProof())
	requireConditionalRefusal(t, err)
	preserved()
	return out.String()
}
