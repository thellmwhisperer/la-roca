// Package resident is the small local transport used by the one-per-machine
// La Roca resident. MCP sessions and one-shot CLI calls are deliberately thin
// clients: only the resident opens SQLite.
package resident

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

var ErrUnavailable = errors.New("resident is not running")

const (
	maxSocketPath = 100
	startupWait   = 10 * time.Second
)

type Options struct {
	Binary  string
	DataDir string
	DBPath  string
	Socket  string
}

type Request struct {
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args,omitempty"`
}

type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type Status struct {
	PID             int              `json:"pid"`
	StartedAt       time.Time        `json:"started_at"`
	UptimeMS        int64            `json:"uptime_ms"`
	AttachedClients int              `json:"attached_clients"`
	OpenConnections int              `json:"open_connections"`
	WAL             map[string]int64 `json:"wal_bytes"`
}

func SocketPath(dataDir string) string {
	if override := strings.TrimSpace(os.Getenv("ROCA_RESIDENT_SOCKET")); override != "" {
		return override
	}
	if strings.TrimSpace(dataDir) == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".roca")
	}
	return filepath.Join(dataDir, "resident", "resident.sock")
}

func (o Options) socket() string {
	if strings.TrimSpace(o.Socket) != "" {
		return o.Socket
	}
	return SocketPath(o.DataDir)
}

func Dial(opts Options) (net.Conn, error) {
	socket := opts.socket()
	if runtime.GOOS != "windows" && len(socket) >= maxSocketPath {
		return nil, fmt.Errorf("resident socket path is too long: %s", socket)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Lstat(socket)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, ErrUnavailable
			}
			return nil, err
		}
		if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("unsafe resident socket: %s", socket)
		}
	}
	conn, err := net.DialTimeout("unix", socket, 200*time.Millisecond)
	if err != nil {
		return nil, ErrUnavailable
	}
	return conn, nil
}

func DialOrSpawn(ctx context.Context, opts Options) (net.Conn, error) {
	if conn, err := Dial(opts); err == nil {
		return conn, nil
	}
	socket := opts.socket()
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return nil, fmt.Errorf("create resident directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Dir(socket), 0o700); err != nil {
			return nil, fmt.Errorf("restrict resident directory: %w", err)
		}
	}
	lockPath := socket + ".lock"
	release, err := securefile.Lock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("lock resident startup: %w", err)
	}
	defer release()
	if conn, err := Dial(opts); err == nil {
		return conn, nil
	}
	if strings.TrimSpace(opts.Binary) == "" {
		return nil, ErrUnavailable
	}
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("replace stale resident socket: %w", err)
	}
	command := exec.Command(opts.Binary, "--db-path", opts.DBPath, "mcp", "serve", "--resident")
	command.Env = os.Environ()
	command.SysProcAttr = detachedProcessAttr()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return nil, fmt.Errorf("open resident input: %w", err)
	}
	defer devNull.Close()
	logDir := filepath.Join(filepath.Dir(socket), "..", "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("create resident log directory: %w", err)
	}
	logFile, err := os.OpenFile(filepath.Join(logDir, "resident.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open resident log: %w", err)
	}
	defer logFile.Close()
	command.Stdin, command.Stdout, command.Stderr = devNull, logFile, logFile
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start resident: %w", err)
	}
	_ = command.Process.Release()
	deadline := time.Now().Add(startupWait)
	var last error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if conn, err := Dial(opts); err == nil {
			return conn, nil
		} else {
			last = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	if last == nil {
		last = ErrUnavailable
	}
	return nil, fmt.Errorf("wait for resident: %w", last)
}

func Call(ctx context.Context, opts Options, op string, args any) (json.RawMessage, error) {
	conn, err := Dial(opts)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode resident request: %w", err)
	}
	if err := json.NewEncoder(conn).Encode(Request{Op: op, Args: payload}); err != nil {
		return nil, fmt.Errorf("send resident request: %w", err)
	}
	var response Response
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return nil, fmt.Errorf("read resident response: %w", err)
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}
	return response.Result, nil
}

// ProxyStdio keeps the MCP process alive when the resident is replaced. MCP
// stdio is newline-delimited JSON, so replaying initialize on a fresh resident
// is sufficient to preserve an attached client session without exposing the
// resident socket to the agent.
func ProxyStdio(ctx context.Context, opts Options, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 64<<20)
	var initialize []byte
	var initialized []byte
	var conn net.Conn
	var reader *bufio.Reader
	var mu sync.Mutex
	closeConn := func() {
		mu.Lock()
		if conn != nil {
			_ = conn.Close()
			conn = nil
			reader = nil
		}
		mu.Unlock()
	}
	connect := func() (net.Conn, error) {
		for {
			candidate, err := DialOrSpawn(ctx, opts)
			if err == nil {
				return candidate, nil
			}
			if !errors.Is(err, ErrUnavailable) {
				return nil, err
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	readMessage := func() ([]byte, error) {
		if reader == nil {
			return nil, ErrUnavailable
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		return line, nil
	}
	var replay func(net.Conn) error
	replay = func(c net.Conn) error {
		if len(initialize) == 0 {
			return nil
		}
		if _, err := c.Write(append(initialize, '\n')); err != nil {
			return err
		}
		if _, err := readMessage(); err != nil { // discard replayed initialize reply
			return err
		}
		if len(initialized) > 0 {
			_, err := c.Write(append(initialized, '\n'))
			return err
		}
		return nil
	}
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var envelope struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(line, &envelope)
		if envelope.Method == "initialize" {
			initialize = append([]byte(nil), line...)
		}
		if envelope.Method == "notifications/initialized" {
			initialized = append([]byte(nil), line...)
		}
		for attempt := 0; attempt < 2; attempt++ {
			if conn == nil {
				candidate, err := connect()
				if err != nil {
					return err
				}
				conn = candidate
				reader = bufio.NewReader(conn)
				if envelope.Method != "initialize" {
					if err := replay(conn); err != nil {
						closeConn()
						continue
					}
				}
			}
			if _, err := conn.Write(append(line, '\n')); err != nil {
				closeConn()
				continue
			}
			if len(envelope.ID) == 0 || envelope.Method != "" && strings.HasPrefix(envelope.Method, "notifications/") {
				break
			}
			for {
				response, err := readMessage()
				if err != nil {
					closeConn()
					break
				}
				var reply struct {
					ID json.RawMessage `json:"id"`
				}
				_ = json.Unmarshal(response, &reply)
				if len(reply.ID) == 0 || string(reply.ID) != string(envelope.ID) {
					if _, err := out.Write(response); err != nil {
						return err
					}
					continue
				}
				if _, err := out.Write(response); err != nil {
					return err
				}
				break
			}
			if conn != nil {
				break
			}
		}
	}
	closeConn()
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}
