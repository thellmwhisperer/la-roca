//go:build linux

package rocavector

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const linuxUserHZ = 100

func processStartUnixNano(pid int) (int64, error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	cut := bytes.LastIndexByte(stat, ')')
	if cut < 0 {
		return 0, fmt.Errorf("parse /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(stat[cut+1:]))
	if len(fields) < 20 {
		return 0, fmt.Errorf("parse /proc/%d/stat", pid)
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, err
	}
	boot, err := linuxBootTimeUnix()
	if err != nil {
		return 0, err
	}
	return boot*1e9 + int64(startTicks)*1e9/linuxUserHZ, nil
}

func linuxBootTimeUnix() (int64, error) {
	body, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if value, ok := strings.CutPrefix(line, "btime "); ok {
			return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		}
	}
	return 0, fmt.Errorf("read Linux boot time")
}
