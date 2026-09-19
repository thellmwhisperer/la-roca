// Package resident provides the local transport for La Roca's database
// resident. Client routing and lifecycle are documented in docs/mcp.md.
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
	"time"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

var ErrUnavailable = errors.New("resident is not running")
var ErrDatabaseMismatch = errors.New("resident is serving a different database")

const (
	maxSocketPath = 100
	startupWait   = 10 * time.Second
)

type Options struct {
	Binary   string
	DataDir  string
	DBPath   string
	Socket   string
	ReadOnly bool
}

type Request struct {
	DBPath   string          `json:"db_path,omitempty"`
	ReadOnly bool            `json:"read_only,omitempty"`
	Op       string          `json:"op"`
	Args     json.RawMessage `json:"args,omitempty"`
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
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if err := json.NewEncoder(conn).Encode(Request{Op: "connect", DBPath: opts.DBPath, ReadOnly: opts.ReadOnly}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("connect to resident: %w", err)
	}
	var response Response
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		conn.Close()
		return nil, fmt.Errorf("identify resident: %w", err)
	}
	if response.Error != "" {
		conn.Close()
		if response.Error == ErrDatabaseMismatch.Error() {
			return nil, ErrDatabaseMismatch
		}
		return nil, errors.New(response.Error)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func DialOrSpawn(ctx context.Context, opts Options) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, startupWait)
	defer cancel()
	if conn, err := Dial(opts); err == nil {
		return conn, nil
	} else if !errors.Is(err, ErrUnavailable) {
		return nil, err
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
	} else if !errors.Is(err, ErrUnavailable) {
		return nil, err
	}
	if strings.TrimSpace(opts.Binary) == "" {
		return nil, ErrUnavailable
	}
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("replace stale resident socket: %w", err)
	}
	args := []string{"--db-path", opts.DBPath, "mcp", "serve", "--resident"}
	if opts.ReadOnly {
		args = append(args, "--read-only")
	}
	command := exec.Command(opts.Binary, args...)
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
			return nil, fmt.Errorf("wait for resident startup; inspect resident.log: %w", err)
		}
		if conn, err := Dial(opts); err == nil {
			return conn, nil
		} else if !errors.Is(err, ErrUnavailable) {
			return nil, err
		} else {
			last = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	if last == nil {
		last = ErrUnavailable
	}
	return nil, fmt.Errorf("resident startup timed out; inspect resident.log: %w", last)
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type message struct {
		line []byte
		err  error
		conn net.Conn
	}
	input := make(chan message)
	go func() {
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 64*1024), 64<<20)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			select {
			case input <- message{line: bytes.Clone(line)}:
			case <-ctx.Done():
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		select {
		case input <- message{err: err}:
		case <-ctx.Done():
		}
	}()
	responses := make(chan message)
	var conn net.Conn
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	var initialize, initialized []byte
	pending := map[string]json.RawMessage{}
	connect := func(replay bool) error {
		candidate, err := DialOrSpawn(ctx, opts)
		if err != nil {
			return err
		}
		reader := bufio.NewReader(candidate)
		if replay && len(initialize) > 0 {
			_ = candidate.SetDeadline(time.Now().Add(startupWait))
			if _, err = candidate.Write(append(bytes.Clone(initialize), '\n')); err == nil {
				_, err = reader.ReadBytes('\n')
			}
			if err == nil && len(initialized) > 0 {
				_, err = candidate.Write(append(bytes.Clone(initialized), '\n'))
			}
			if err != nil {
				candidate.Close()
				return err
			}
			_ = candidate.SetDeadline(time.Time{})
		}
		conn = candidate
		go func() {
			for {
				line, err := reader.ReadBytes('\n')
				select {
				case responses <- message{line: line, err: err, conn: candidate}:
				case <-ctx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
		return nil
	}
	disconnect := func() error {
		conn.Close()
		conn = nil
		for _, id := range pending {
			if err := json.NewEncoder(out).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"error": map[string]any{"code": -32000, "message": "resident disconnected; request outcome is unknown"},
			}); err != nil {
				return err
			}
		}
		clear(pending)
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case received := <-input:
			if received.err != nil {
				if errors.Is(received.err, io.EOF) {
					return nil
				}
				return received.err
			}
			var envelope struct {
				Method string          `json:"method"`
				ID     json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(received.line, &envelope)
			if conn == nil {
				if err := connect(envelope.Method != "initialize"); err != nil {
					return err
				}
			}
			if envelope.Method == "initialize" {
				initialize = bytes.Clone(received.line)
			}
			if envelope.Method == "notifications/initialized" {
				initialized = bytes.Clone(received.line)
			}
			if len(envelope.ID) > 0 && envelope.Method != "" {
				pending[string(envelope.ID)] = envelope.ID
			}
			if _, err := conn.Write(append(received.line, '\n')); err != nil {
				if err := disconnect(); err != nil {
					return err
				}
			}
		case received := <-responses:
			if received.conn != conn {
				continue
			}
			if received.err != nil {
				if err := disconnect(); err != nil {
					return err
				}
				continue
			}
			var envelope struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(received.line, &envelope)
			if envelope.Method == "" {
				delete(pending, string(envelope.ID))
			}
			if _, err := out.Write(received.line); err != nil {
				return err
			}
		}
	}
}
