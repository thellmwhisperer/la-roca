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
	if !builtIn(root, "ingest") {
		t.Error("the bundled corpus manifest did not seat its ingest verb")
	}
	if builtIn(root, "receipts") {
		t.Error("the documented third-party verb took a seat this build does not register")
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
