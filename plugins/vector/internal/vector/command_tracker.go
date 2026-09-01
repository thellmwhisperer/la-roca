package vector

import (
	"context"
	"sync"
)

type commandTrackerKey struct{}

type commandTracker struct {
	mu       sync.Mutex
	active   int
	stopping bool
	done     chan struct{}
}

func WithWorkerCommandDrain(ctx context.Context) (context.Context, func()) {
	tracker := &commandTracker{done: make(chan struct{})}
	return context.WithValue(ctx, commandTrackerKey{}, tracker), tracker.stopAndWait
}

func beginTrackedCommand(ctx context.Context) (func(), error) {
	tracker, _ := ctx.Value(commandTrackerKey{}).(*commandTracker)
	if tracker == nil {
		return func() {}, nil
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.stopping {
		return nil, context.Canceled
	}
	tracker.active++
	return tracker.finish, nil
}

func (t *commandTracker) finish() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active--
	if t.stopping && t.active == 0 {
		select {
		case <-t.done:
		default:
			close(t.done)
		}
	}
}

func (t *commandTracker) stopAndWait() {
	t.mu.Lock()
	t.stopping = true
	if t.active == 0 {
		select {
		case <-t.done:
		default:
			close(t.done)
		}
	}
	done := t.done
	t.mu.Unlock()
	<-done
}
