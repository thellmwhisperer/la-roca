package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

var errResidentAlreadyRunning = errors.New("semantic search resident is already running")

func runSharedResident(ctx context.Context, env *environment, socket string, idle time.Duration) error {
	listener, err := listenResidentSocket(socket)
	if err != nil {
		if errors.Is(err, errResidentAlreadyRunning) {
			return nil
		}
		return err
	}
	defer listener.Close()
	session, err := newResidentSession(ctx, env)
	if err != nil {
		return err
	}
	if idle <= 0 {
		idle = defaultResidentIdle
	}
	return serveListeningResident(ctx, listener, idle, session)
}

func listenResidentSocket(path string) (net.Listener, error) {
	if path == "" {
		return nil, fmt.Errorf("semantic search resident socket is required")
	}
	if conn, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		_ = conn.Close()
		return nil, errResidentAlreadyRunning
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = listener.Close()
			return nil, err
		}
	}
	return listener, nil
}

func serveListeningResident(ctx context.Context, listener net.Listener, idle time.Duration, session residentSession) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu       sync.Mutex
		clients  = make(map[net.Conn]struct{})
		gen      uint64
		stopping bool
		done     = make(chan struct{})
	)
	stopLocked := func() {
		if stopping {
			return
		}
		stopping = true
		close(done)
		cancel()
		_ = listener.Close()
		for conn := range clients {
			_ = conn.Close()
		}
	}
	stop := func() {
		mu.Lock()
		defer mu.Unlock()
		stopLocked()
	}
	armLocked := func() {
		gen++
		id := gen
		time.AfterFunc(idle, func() {
			mu.Lock()
			defer mu.Unlock()
			if gen == id && len(clients) == 0 {
				stopLocked()
			}
		})
	}
	defer stop()
	go func() {
		select {
		case <-ctx.Done():
			stop()
		case <-done:
		}
	}()
	mu.Lock()
	armLocked()
	mu.Unlock()
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-done:
				return nil
			default:
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
		mu.Lock()
		if stopping {
			mu.Unlock()
			_ = conn.Close()
			return nil
		}
		clients[conn] = struct{}{}
		gen++
		mu.Unlock()
		go func() {
			defer func() {
				_ = conn.Close()
				mu.Lock()
				defer mu.Unlock()
				delete(clients, conn)
				if len(clients) == 0 && !stopping {
					armLocked()
				}
			}()
			if err := serveResidentSession(ctx, conn, session); errors.Is(err, errResidentUnusable) {
				stop()
			}
		}()
	}
}
