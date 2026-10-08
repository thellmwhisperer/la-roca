package search_test

import (
	"context"
	"sync"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

// cancelAfter is a context that cancels itself on its nth look at Done, so a
// run can be stopped at every point it checks for cancellation, in turn.
type cancelAfter struct {
	context.Context
	mu   sync.Mutex
	left int
	done chan struct{}
}

func (c *cancelAfter) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.left == 0 {
		close(c.done)
	}
	c.left--
	return c.done
}

func (c *cancelAfter) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}

// A rebuild stopped at any point leaves the previous index answering: the new
// tables are never visible empty.
func TestAnInterruptedRebuildLeavesThePreviousIndexAnswering(t *testing.T) {
	db := seededWorld(t)
	if _, err := search.Index(context.Background(), db, nil); err != nil {
		t.Fatal(err)
	}
	known := map[string]string{"memories_fts": "dashes", "exchanges_fts": "yaml",
		"thinking_fts": "format", "sessions_fts": "roca"}
	for n := 0; n < 10000; n++ {
		_, err := search.Rebuild(&cancelAfter{Context: context.Background(), left: n,
			done: make(chan struct{})}, db)
		for table, word := range known {
			var matches int
			if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+table+` MATCH ?`,
				word).Scan(&matches); err != nil {
				t.Fatalf("cancelled at check %d: search %s: %v", n, table, err)
			}
			if matches == 0 {
				t.Fatalf("cancelled at check %d (%v): %s no longer answers for %q", n, err, table, word)
			}
		}
		if err == nil {
			return
		}
	}
	t.Fatal("the rebuild never finished")
}
