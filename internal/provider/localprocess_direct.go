//go:build !darwin && !linux && !windows

package provider

import (
	"fmt"
	"os/exec"
	"runtime"
)

func runLocalCommand(_ *exec.Cmd) error {
	return fmt.Errorf("local-binary process-tree containment is not supported on %s", runtime.GOOS)
}
