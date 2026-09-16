package plugin

import (
	"fmt"
	"os"
)

func operatorRideFileAllowed(path string, file *os.File, info os.FileInfo, euid int) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("operator ride file %s is not a regular file; refuse to run its rides", path)
	}
	if err := operatorRideFilePermissionsAllowed(file, info); err != nil {
		return fmt.Errorf("operator ride file %s %w", path, err)
	}
	owned, err := operatorRideFileOwnedByUser(file, info, euid)
	if err != nil {
		return fmt.Errorf("operator ride file %s ownership could not be verified; refuse to run its rides: %w", path, err)
	}
	if !owned {
		return fmt.Errorf("operator ride file %s is not owned by the user running roca cron; refuse to run its rides", path)
	}
	return nil
}
