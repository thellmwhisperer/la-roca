//go:build darwin

package vector

import "golang.org/x/sys/unix"

func processStartUnixNano(pid int) (int64, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	return info.Proc.P_starttime.Nano(), nil
}
