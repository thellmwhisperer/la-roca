//go:build linux

package rocavector

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func processStartIdentity(pid int) (string, error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return linuxProcessIdentity(pid, stat, bootID)
}

func linuxProcessIdentity(pid int, stat, rawBootID []byte) (string, error) {
	cut := bytes.LastIndexByte(stat, ')')
	if cut < 0 {
		return "", fmt.Errorf("parse /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(stat[cut+1:]))
	if len(fields) < 20 {
		return "", fmt.Errorf("parse /proc/%d/stat", pid)
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return "", err
	}
	bootID := strings.TrimSpace(string(rawBootID))
	bootFields := strings.Fields(bootID)
	if len(bootFields) != 1 || bootFields[0] != bootID {
		return "", fmt.Errorf("parse Linux boot identity")
	}
	return bootID + ":" + strconv.FormatUint(startTicks, 10), nil
}
