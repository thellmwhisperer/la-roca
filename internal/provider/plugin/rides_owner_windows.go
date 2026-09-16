//go:build windows

package plugin

import (
	"fmt"
	"os"
)

func openOperatorRide(string) (*os.File, error) {
	return nil, fmt.Errorf("operator rides are not supported on Windows")
}

func operatorRideFilePermissionsAllowed(*os.File, os.FileInfo) error {
	return fmt.Errorf("operator rides are not supported on Windows")
}

func operatorRideFileOwnedByUser(*os.File, os.FileInfo, int) (bool, error) {
	return false, fmt.Errorf("operator rides are not supported on Windows")
}
