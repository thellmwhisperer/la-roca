//go:build darwin

package vector

import (
	"strconv"

	"golang.org/x/sys/unix"
)

func processStartIdentity(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(info.Proc.P_starttime.Nano(), 10), nil
}
