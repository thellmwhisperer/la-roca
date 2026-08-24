//go:build !darwin && !linux

package toolcallobserver

import "fmt"

func openOSTerminal(TerminalRequest) error {
	return fmt.Errorf("no terminal program is available on this system")
}
