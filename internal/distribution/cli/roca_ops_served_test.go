package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestRocaOpsServedFollowsTheRoute(t *testing.T) {
	dir := t.TempDir()
	for _, state := range []struct {
		name   string
		config string
		served bool
	}{
		{name: "legacy roca_ops absent", config: "[features]\nvector = true\n"},
		{name: "legacy roca_ops false", config: "[features]\nroca_ops = false\n[layout]\nserving = \"legacy-serving\"\n"},
		{name: "legacy roca_ops true", config: "[features]\nroca_ops = true\n", served: true},
		{name: "cutover", config: "[features]\nroca_ops = false\n[layout]\nserving = \"cutover\"\n", served: true},
	} {
		t.Run(state.name, func(t *testing.T) {
			path := filepath.Join(dir, "config.toml")
			if err := os.WriteFile(path, []byte(state.config), 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := config.LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			served, err := rocaOpsServed(file, filepath.Join(dir, "absent-roca-ops.db"))
			if err != nil || served != state.served {
				t.Fatalf("rocaOpsServed = %v, %v; want %v", served, err, state.served)
			}
		})
	}
}
