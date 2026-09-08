package vectorresident

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

const maxUnixSocketPath = 100

// DialOrSpawn connects to the shared embedding resident, starting it when
// nothing is listening. An empty Binary refuses to spawn and returns
// ErrUnavailable so a one-shot caller can fall back.
func DialOrSpawn(ctx context.Context, opts Options) (io.ReadWriteCloser, error) {
	socket, lockPath := opts.socketAndLock()
	if runtime.GOOS != "windows" && len(socket) >= maxUnixSocketPath {
		return nil, fmt.Errorf("semantic search resident socket path is too long: %s", socket)
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return nil, fmt.Errorf("create semantic search resident directory: %w", err)
	}
	if err := validateResidentDirectory(filepath.Dir(socket)); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create semantic search resident directory: %w", err)
	}
	if err := validateResidentDirectory(filepath.Dir(lockPath)); err != nil {
		return nil, err
	}
	if conn, err := Dial(socket); err == nil {
		return conn, nil
	}
	if strings.TrimSpace(opts.Binary) == "" {
		return nil, ErrUnavailable
	}

	release, err := securefile.Lock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("lock semantic search resident: %w", err)
	}
	defer func() { _ = release() }()
	if conn, err := Dial(socket); err == nil {
		return conn, nil
	}
	if err := spawnResident(opts, socket); err != nil {
		return nil, err
	}
	return waitResidentSocket(ctx, socket)
}

// Dial connects to an already listening resident socket after ownership checks.
func Dial(socket string) (io.ReadWriteCloser, error) {
	return dialUnixTimeout(socket, 200*time.Millisecond)
}

func waitResidentSocket(ctx context.Context, socket string) (io.ReadWriteCloser, error) {
	deadline := time.Now().Add(10 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conn, err := Dial(socket)
		if err == nil {
			return conn, nil
		}
		last = err
		time.Sleep(20 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timed out")
	}
	return nil, fmt.Errorf("wait for semantic search resident: %w", last)
}

func spawnResident(opts Options, socket string) error {
	args := []string{"_resident", "--listen", socket}
	if path := strings.TrimSpace(opts.DBPath); path != "" {
		args = append([]string{"--db-path", path}, args...)
	}
	command := exec.Command(opts.Binary, args...)
	command.Env = append(os.Environ(), spawnEnv(opts)...)
	command.SysProcAttr = detachedResidentAttr()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open semantic search input: %w", err)
	}
	defer devNull.Close()
	logFile, err := residentLogFile(opts, socket)
	if err != nil {
		return err
	}
	defer logFile.Close()
	command.Stdin, command.Stdout, command.Stderr = devNull, logFile, logFile
	if err := command.Start(); err != nil {
		return fmt.Errorf("start semantic search: %w", err)
	}
	if command.Process != nil {
		_ = command.Process.Release()
	}
	return nil
}

func spawnEnv(opts Options) []string {
	env := []string{}
	if strings.TrimSpace(os.Getenv("ROCA_VECTOR_ROCA_BINARY")) == "" && strings.TrimSpace(opts.HostBinary) != "" {
		env = append(env, "ROCA_VECTOR_ROCA_BINARY="+opts.HostBinary)
	}
	if root := strings.TrimSpace(opts.PluginRoot); root != "" {
		env = append(env, "ROCA_VECTOR_PLUGIN_ROOT="+root)
	}
	if state := strings.TrimSpace(opts.StateDir); state != "" {
		env = append(env, "ROCA_VECTOR_STATE_DIR="+state)
	} else if dataDir := strings.TrimSpace(opts.DataDir); dataDir != "" {
		env = append(env, "ROCA_VECTOR_STATE_DIR="+filepath.Join(dataDir, "plugins", "roca-vector", "state"))
	}
	return env
}

func residentLogFile(opts Options, socket string) (*os.File, error) {
	dir := filepath.Dir(socket)
	if dataDir := strings.TrimSpace(opts.DataDir); dataDir != "" {
		dir = filepath.Join(dataDir, "logs")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create semantic search log directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "vector-resident.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open semantic search log: %w", err)
	}
	return file, nil
}

// PayloadPath is the roca-vector executable next to this process, on PATH, or
// the ROCA_VECTOR_RESIDENT_BINARY override.
func PayloadPath() string {
	if override := strings.TrimSpace(os.Getenv("ROCA_VECTOR_RESIDENT_BINARY")); override != "" {
		return override
	}
	name := "roca-vector"
	if runtime.GOOS == "windows" {
		name = "roca-vector.exe"
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
}

// NamedPluginBinary reports whether path is the installed roca-vector plugin
// (not a test binary whose name merely starts with that prefix).
func NamedPluginBinary(path string) bool {
	base := filepath.Base(path)
	return base == "roca-vector" || base == "roca-vector.exe"
}
