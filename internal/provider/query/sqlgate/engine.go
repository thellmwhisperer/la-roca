package sqlgate

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"modernc.org/libc"
	"modernc.org/libc/sys/types"
	sqlite3 "modernc.org/sqlite/lib"
)

var ptrSize = types.Size_t(unsafe.Sizeof(uintptr(0)))

func cFuncPointer[T any](f T) uintptr {
	return *(*uintptr)(unsafe.Pointer(&struct{ f T }{f}))
}

// engine is the gate's own schema-only in-memory connection, opened against
// modernc.org/sqlite/lib so an authorization callback can be attached here
// without touching the application's query connection.
type engine struct {
	tls    *libc.TLS
	db     uintptr
	mu     sync.Mutex
	denial string
}

var engines = struct {
	mu sync.RWMutex
	m  map[uintptr]*engine
}{m: make(map[uintptr]*engine)}

func mallocSlot(tls *libc.TLS) (uintptr, error) {
	p := libc.Xmalloc(tls, ptrSize)
	if p == 0 {
		return 0, fmt.Errorf("out of memory")
	}
	return p, nil
}

func loadSlot(tls *libc.TLS, p uintptr) uintptr {
	var slot uintptr
	libc.Xmemcpy(tls, uintptr(unsafe.Pointer(&slot)), p, ptrSize)
	runtime.KeepAlive(&slot)
	return slot
}

func openEngine() (*engine, error) {
	tls := libc.NewTLS()
	name, err := libc.CString(":memory:")
	if err != nil {
		tls.Close()
		return nil, fmt.Errorf("open the validation database: %w", err)
	}
	defer libc.Xfree(tls, name)

	p, err := mallocSlot(tls)
	if err != nil {
		tls.Close()
		return nil, fmt.Errorf("open the validation database: %w", err)
	}
	defer libc.Xfree(tls, p)

	rc := sqlite3.Xsqlite3_open_v2(tls, name, p,
		sqlite3.SQLITE_OPEN_READWRITE|sqlite3.SQLITE_OPEN_CREATE|sqlite3.SQLITE_OPEN_FULLMUTEX, 0)
	db := loadSlot(tls, p)
	if rc != sqlite3.SQLITE_OK {
		msg := fmt.Sprintf("sqlite open failed (%d)", rc)
		if db != 0 {
			msg = libc.GoString(sqlite3.Xsqlite3_errmsg(tls, db))
			sqlite3.Xsqlite3_close_v2(tls, db)
		}
		tls.Close()
		return nil, fmt.Errorf("open the validation database: %s", msg)
	}
	return &engine{tls: tls, db: db}, nil
}

func (e *engine) close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.db == 0 {
		return nil
	}
	engines.mu.Lock()
	delete(engines.m, e.db)
	engines.mu.Unlock()
	sqlite3.Xsqlite3_set_authorizer(e.tls, e.db, 0, 0)
	rc := sqlite3.Xsqlite3_close_v2(e.tls, e.db)
	e.db = 0
	e.tls.Close()
	e.tls = nil
	if rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("close the validation database: %d", rc)
	}
	return nil
}

func (e *engine) exec(sql string) error {
	z, err := libc.CString(sql)
	if err != nil {
		return err
	}
	defer libc.Xfree(e.tls, z)
	if rc := sqlite3.Xsqlite3_exec(e.tls, e.db, z, 0, 0, 0); rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("%s", libc.GoString(sqlite3.Xsqlite3_errmsg(e.tls, e.db)))
	}
	return nil
}

func (e *engine) attachAuthorizer() error {
	engines.mu.Lock()
	engines.m[e.db] = e
	engines.mu.Unlock()
	rc := sqlite3.Xsqlite3_set_authorizer(e.tls, e.db, cFuncPointer(authorizerTrampoline), e.db)
	if rc != sqlite3.SQLITE_OK {
		engines.mu.Lock()
		delete(engines.m, e.db)
		engines.mu.Unlock()
		return fmt.Errorf("attach the authorization callback: %s", libc.GoString(sqlite3.Xsqlite3_errmsg(e.tls, e.db)))
	}
	return nil
}

func authorizerTrampoline(_ *libc.TLS, pArg uintptr, action int32, arg1, arg2, _, _ uintptr) int32 {
	engines.mu.RLock()
	e := engines.m[pArg]
	engines.mu.RUnlock()
	if e == nil {
		return sqlite3.SQLITE_DENY
	}
	return e.authorize(action, libc.GoString(arg1), libc.GoString(arg2))
}

func (e *engine) authorize(action int32, arg1, arg2 string) int32 {
	switch action {
	case sqlite3.SQLITE_SELECT, sqlite3.SQLITE_RECURSIVE:
		return sqlite3.SQLITE_OK
	case sqlite3.SQLITE_READ:
		if IsHiddenTable(arg1) {
			e.note(fmt.Sprintf("no such table: %q is not a table this query can read", arg1))
			return sqlite3.SQLITE_DENY
		}
		return sqlite3.SQLITE_OK
	case sqlite3.SQLITE_FUNCTION:
		if allowedFunctions[strings.ToLower(arg2)] {
			return sqlite3.SQLITE_OK
		}
		e.note(fmt.Sprintf("Function %q is not allowed", arg2))
		return sqlite3.SQLITE_DENY
	default:
		e.note("Only SELECT statements are allowed")
		return sqlite3.SQLITE_DENY
	}
}

func (e *engine) note(msg string) {
	if e.denial == "" {
		e.denial = msg
	}
}

func (e *engine) prepare(sql string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.denial = ""

	z, err := libc.CString(sql)
	if err != nil {
		return err
	}
	defer libc.Xfree(e.tls, z)

	pp, err := mallocSlot(e.tls)
	if err != nil {
		return err
	}
	defer libc.Xfree(e.tls, pp)

	rc := sqlite3.Xsqlite3_prepare_v2(e.tls, e.db, z, -1, pp, 0)
	stmt := loadSlot(e.tls, pp)
	if stmt != 0 {
		sqlite3.Xsqlite3_finalize(e.tls, stmt)
	}
	if rc == sqlite3.SQLITE_OK {
		return nil
	}
	if e.denial != "" {
		return fmt.Errorf("%s", e.denial)
	}
	return fmt.Errorf("%s", translate(libc.GoString(sqlite3.Xsqlite3_errmsg(e.tls, e.db))))
}
