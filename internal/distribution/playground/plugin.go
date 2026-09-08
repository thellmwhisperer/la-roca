// Package playground dispatches the optional answering executable.
package playground

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const InstallHint = "install the optional playground plugin: roca plugin install thellmwhisperer/roca-playground"

func Executable() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".roca", "plugins", "roca-playground", "roca-playground")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0) {
		return "", fmt.Errorf("%s", InstallHint)
	}
	return path, nil
}

func Run(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
	path, err := Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, stderr
	return cmd.Run()
}

func JSON(ctx context.Context, args []string, result any) error {
	path, err := Executable()
	if err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, path, append(args, "--json")...).Output()
	if len(out) == 0 && err != nil {
		return err
	}
	return json.Unmarshal(out, result)
}
