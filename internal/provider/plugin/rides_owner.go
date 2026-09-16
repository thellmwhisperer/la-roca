package plugin

import (
	"fmt"
	"os"
)

// CheckOperatorRideFile is the only operator-ride gate: the file the operator
// wrote is the consent. The user running roca cron must own it, and group or
// other must not be able to write it.
func CheckOperatorRideFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("operator ride file %s could not be inspected: %w", path, err)
	}
	return operatorRideFileAllowed(path, info, os.Geteuid())
}

func operatorRideFileAllowed(path string, info os.FileInfo, euid int) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("operator ride file %s is a symlink; refuse to run its rides", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("operator ride file %s is not a regular file; refuse to run its rides", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("operator ride file %s is writable by group or others; refuse to run its rides", path)
	}
	uid, ok := fileOwnerUID(info)
	if ok && uid != euid {
		return fmt.Errorf("operator ride file %s is not owned by the user running roca cron; refuse to run its rides", path)
	}
	return nil
}
