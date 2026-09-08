//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
)

func installPlaygroundForAcceptance(home string) error {
	source := os.Getenv("ROCA_PLAYGROUND_BIN")
	if source == "" {
		return fmt.Errorf("this integration scenario requires ROCA_PLAYGROUND_BIN; run make accept")
	}
	payload, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".roca", "plugins", "roca-playground")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "roca-playground"), payload, 0700)
}
