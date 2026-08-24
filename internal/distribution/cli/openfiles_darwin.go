//go:build darwin

package cli

import (
	"os/exec"
	"strconv"
	"strings"
)

func openFilesOf(pid int) []string {
	output, err := exec.Command("lsof", "-p", strconv.Itoa(pid), "-Fn").Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "n") && len(line) > 1 {
			files = append(files, line[1:])
		}
	}
	return files
}
