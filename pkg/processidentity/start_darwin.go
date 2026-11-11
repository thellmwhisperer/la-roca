//go:build darwin

package processidentity

import (
	"strconv"

	"golang.org/x/sys/unix"
)

func Start(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(info.Proc.P_starttime.Nano(), 10), nil
}
