// Package vectorresident is the shared client for the embedding resident:
// one process holds the model, and both MCP and the CLI query it.
package vectorresident

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnavailable means nothing is listening and this caller will not start one.
var ErrUnavailable = errors.New("semantic search resident is not listening")

// Options name the data directory, the plugin binary, and the optional socket
// override used to reach the shared embedding resident.
type Options struct {
	Binary     string
	HostBinary string
	DataDir    string
	DBPath     string
	PluginRoot string
	StateDir   string
	Socket     string
	Lock       string
	Status     io.Writer
}

// SocketPaths returns the listen socket and startup lock for a data directory.
// ROCA_VECTOR_RESIDENT_SOCKET overrides both, placing the lock beside it.
func SocketPaths(dataDir string) (socket, lock string) {
	dir := strings.TrimSpace(dataDir)
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".roca")
	}
	dir = filepath.Join(dir, "vector-resident")
	lock = filepath.Join(dir, "resident.lock")
	if override := strings.TrimSpace(os.Getenv("ROCA_VECTOR_RESIDENT_SOCKET")); override != "" {
		return override, override + ".lock"
	}
	return filepath.Join(dir, "resident.sock"), lock
}

func (o Options) socketAndLock() (socket, lock string) {
	if strings.TrimSpace(o.Socket) != "" {
		socket = o.Socket
		lock = strings.TrimSpace(o.Lock)
		if lock == "" {
			lock = socket + ".lock"
		}
		return socket, lock
	}
	return SocketPaths(o.DataDir)
}

func currentQueryPaths(opts Options) (socket, lock string) {
	socket, lock = opts.socketAndLock()
	return socket + ".current", lock + ".current"
}
