package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A manifest verb takes a CLI seat only for bundled plugins in this build, so the
// published contract may not promise a third-party author a registered command.
func TestOnlyBundledManifestVerbsTakeACLISeat(t *testing.T) {
	root := rootCommand(&cliEnv{})
	seats := []struct {
		verb   string
		seated bool
		reason string
	}{
		{"ingest", true, "the bundled corpus manifest did not seat its ingest verb"},
		{"query", true, "the bundled ops manifest did not seat its query verb"},
		{"exec", true, "the bundled ops manifest did not seat its exec verb"},
		{"store", true, "the bundled ops manifest did not seat its store verb"},
		{"sql", false, "the sql verb rides `playground --sql-only` and owns no command of its own"},
		{"receipts", false, "the documented third-party verb took a seat this build does not register"},
	}
	for _, seat := range seats {
		if builtIn(root, seat.verb) != seat.seated {
			t.Error(seat.reason)
		}
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "plugins.md"))
	if err != nil {
		t.Fatal(err)
	}
	docs := string(raw)
	if !strings.Contains(docs, "bundled manifest plugins") {
		t.Error("the plugin contract does not scope CLI verb registration to bundled manifests")
	}
	if !strings.Contains(docs, "`roca-<verb>` executable") {
		t.Error("the plugin contract does not say how a third-party verb reaches the CLI today")
	}
}
