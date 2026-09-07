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
	"sync/atomic"
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
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socket)
	}()
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
			_ = os.Remove(path)
			return nil, err
		}
	}
	return listener, nil
}

func serveListeningResident(ctx context.Context, listener net.Listener, idle time.Duration, session residentSession) error {
	var (
		clients atomic.Int64
		gen     atomic.Uint64
		once    sync.Once
		done    = make(chan struct{})
	)
	stop := func() {
		once.Do(func() {
			close(done)
			_ = listener.Close()
		})
	}
	arm := func() {
		id := gen.Add(1)
		time.AfterFunc(idle, func() {
			if gen.Load() == id && clients.Load() == 0 {
				stop()
			}
		})
	}
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()
	arm()
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
		clients.Add(1)
		gen.Add(1)
		go func() {
			defer func() {
				if clients.Add(-1) == 0 {
					arm()
				}
				_ = conn.Close()
			}()
			_ = serveResidentSession(ctx, conn, session)
		}()
	}
}
