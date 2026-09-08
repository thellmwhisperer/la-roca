// Package playground dispatches the optional answering executable.
package playground

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// Audit is the row-free metadata sent separately from the plugin's live stdout.
type Audit struct {
	Stderr     string               `json:"stderr"`
	Query      *service.QueryResult `json:"query,omitempty"`
	CleanedSQL string               `json:"cleaned_sql,omitempty"`
}

func Run(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) (*service.QueryResult, error) {
	path, err := Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, stderr
	if len(args) > 0 && (args[0] == "model" || args[0] == "models" || args[0] == "login") {
		return nil, cmd.Run()
	}
	var wire bytes.Buffer
	cmd.Args = append([]string{path, "--transport"}, args...)
	cmd.Stderr = &wire
	err = cmd.Run()
	var audit Audit
	if decodeErr := json.Unmarshal(wire.Bytes(), &audit); decodeErr != nil {
		_, _ = stderr.Write(wire.Bytes())
		if err == nil {
			err = fmt.Errorf("playground audit transport: %w", decodeErr)
		}
		return nil, err
	}
	_, _ = io.WriteString(stderr, audit.Stderr)
	if audit.Query != nil {
		audit.Query.CleanedSQL = audit.CleanedSQL
	}
	return audit.Query, err
}

func JSON(ctx context.Context, args []string, result any) error {
	var out []byte
	var stderr bytes.Buffer
	var err error
	var audit *service.QueryResult
	if _, query := result.(*service.QueryResult); query {
		var stdout bytes.Buffer
		audit, err = Run(ctx, append([]string{"--json"}, args...), nil, &stdout, &stderr)
		out = stdout.Bytes()
	} else {
		path, resolveErr := Executable()
		if resolveErr != nil {
			return resolveErr
		}
		cmd := exec.CommandContext(ctx, path, append(args, "--json")...)
		cmd.Stderr = &stderr
		out, err = cmd.Output()
	}
	var exited *exec.ExitError
	if err != nil && (len(out) == 0 || !errors.As(err, &exited)) {
		if diagnostic := strings.TrimSpace(stderr.String()); diagnostic != "" {
			return fmt.Errorf("%w: %s", err, diagnostic)
		}
		return err
	}
	if err := json.Unmarshal(out, result); err != nil {
		return err
	}
	if query, ok := result.(*service.QueryResult); ok && audit != nil {
		query.CleanedSQL = audit.CleanedSQL
	}
	return nil
}
