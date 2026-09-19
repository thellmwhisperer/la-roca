package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

func TestBoundConnectionCancelsAScanAfterTheFirstRow(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "roca.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	unbounded := timeScan(t, db, context.Background(), false)
	if unbounded < 80*time.Millisecond {
		t.Fatalf("unbounded scan finished in %s; the fixture is too small to prove the bound", unbounded)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	bounded := timeScan(t, db, ctx, true)
	if bounded > time.Second {
		t.Fatalf("bounded scan ran %s, want <= 1s (unbounded %s)", bounded, unbounded)
	}
	if bounded >= unbounded {
		t.Fatalf("bounded scan %s did not beat unbounded %s", bounded, unbounded)
	}
}

func timeScan(t *testing.T, db *store.DB, ctx context.Context, bind bool) time.Duration {
	t.Helper()
	conn, err := db.SQL().Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if bind {
		restore, err := store.BoundConnection(ctx, conn)
		if err != nil {
			t.Fatal(err)
		}
		defer restore()
	}
	started := time.Now()
	rows, err := conn.QueryContext(ctx, `WITH RECURSIVE t(n) AS (
		SELECT 1 UNION ALL SELECT n+1 FROM t WHERE n < 8000000
	) SELECT n FROM t`)
	if err == nil {
		for rows.Next() {
			var n int
			_ = rows.Scan(&n)
		}
		err = rows.Err()
		_ = rows.Close()
	}
	elapsed := time.Since(started)
	if bind {
		if ctx.Err() == nil && err == nil {
			t.Fatalf("bounded scan completed without cancellation after %s", elapsed)
		}
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) &&
			!errors.Is(err, context.DeadlineExceeded) &&
			!strings.Contains(errString(err), "INTERRUPT") {
			t.Fatalf("bounded scan err = %v ctx = %v", err, ctx.Err())
		}
	} else if err != nil {
		t.Fatalf("unbounded scan: %v", err)
	}
	return elapsed
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
