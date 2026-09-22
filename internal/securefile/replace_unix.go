//go:build darwin || linux

package securefile

import "os"

func renameReplace(staged, target string) error {
	return os.Rename(staged, target)
}
