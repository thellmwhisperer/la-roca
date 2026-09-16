//go:build windows

package plugin

import "os"

func openOperatorRide(path string, _ bool) (*os.File, error) {
	return os.Open(path)
}
