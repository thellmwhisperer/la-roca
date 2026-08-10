package config_test

import (
	"os"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestResolveNamesAPathWithoutCreatingOrSelectingADatabase(t *testing.T) {
	paths, err := config.Resolve(config.Input{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.DB); !os.IsNotExist(err) {
		t.Fatalf("Resolve created or selected a database at %s", paths.DB)
	}
}
