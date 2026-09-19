package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sync"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

const queryProgressOps = 100

var queryBounds = struct {
	mu sync.RWMutex
	m  map[uintptr]context.Context
}{m: make(map[uintptr]context.Context)}

// BoundConnection makes a cancelled context abort the statement running on
// conn, including scans that continue after QueryContext has already returned
// the first row. The driver only interrupts until that first row; exec needs
// the bound for the whole result.
func BoundConnection(ctx context.Context, conn *sql.Conn) (func(), error) {
	if conn == nil {
		return func() {}, fmt.Errorf("bind the query time limit: connection is nil")
	}
	if ctx.Done() == nil {
		return func() {}, nil
	}
	var restore func()
	err := conn.Raw(func(driverConn any) error {
		tls, db, ok := sqliteConnHandles(driverConn)
		if !ok {
			return fmt.Errorf("bind the query time limit: unsupported SQLite connection")
		}
		restore = installQueryBound(ctx, tls, db)
		return nil
	})
	if err != nil {
		return func() {}, err
	}
	if restore == nil {
		return func() {}, nil
	}
	return restore, nil
}

func sqliteConnHandles(driverConn any) (*libc.TLS, uintptr, bool) {
	value := reflect.ValueOf(driverConn)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		return nil, 0, false
	}
	value = value.Elem()
	if value.Kind() != reflect.Struct {
		return nil, 0, false
	}
	dbField := value.FieldByName("db")
	tlsField := value.FieldByName("tls")
	if !dbField.IsValid() || !tlsField.IsValid() || dbField.Kind() != reflect.Uintptr {
		return nil, 0, false
	}
	db := uintptr(reflect.NewAt(dbField.Type(), unsafe.Pointer(dbField.UnsafeAddr())).Elem().Uint())
	tls := *(**libc.TLS)(unsafe.Pointer(tlsField.UnsafeAddr()))
	return tls, db, tls != nil && db != 0
}

func cFuncPointer[T any](fn T) uintptr {
	return *(*uintptr)(unsafe.Pointer(&struct{ fn T }{fn}))
}

func installQueryBound(ctx context.Context, tls *libc.TLS, db uintptr) func() {
	queryBounds.mu.Lock()
	queryBounds.m[db] = ctx
	queryBounds.mu.Unlock()
	sqlite3.Xsqlite3_progress_handler(tls, db, queryProgressOps, cFuncPointer(queryProgressTrampoline), db)
	stopInterrupt := interruptWhenDone(ctx, db)
	var once sync.Once
	return func() {
		once.Do(func() {
			stopInterrupt()
			sqlite3.Xsqlite3_progress_handler(tls, db, 0, 0, 0)
			queryBounds.mu.Lock()
			delete(queryBounds.m, db)
			queryBounds.mu.Unlock()
		})
	}
}

func queryProgressTrampoline(_ *libc.TLS, pArg uintptr) int32 {
	queryBounds.mu.RLock()
	ctx := queryBounds.m[pArg]
	queryBounds.mu.RUnlock()
	if ctx != nil && ctx.Err() != nil {
		return 1
	}
	return 0
}

func interruptWhenDone(ctx context.Context, db uintptr) func() {
	done := make(chan struct{})
	var mu sync.Mutex
	var stopped bool
	go func() {
		select {
		case <-ctx.Done():
			mu.Lock()
			if !stopped {
				tls := libc.NewTLS()
				sqlite3.Xsqlite3_interrupt(tls, db)
				tls.Close()
			}
			mu.Unlock()
		case <-done:
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			mu.Lock()
			stopped = true
			mu.Unlock()
			close(done)
		})
	}
}
