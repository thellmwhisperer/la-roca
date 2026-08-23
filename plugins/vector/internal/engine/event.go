// Package engine is the local embedding runtime's structural feedback
// envelope. Progress, partial results, completion, errors and cancellation
// share one shape so callers never bolt status onto the side of a return.
package engine

import (
	"fmt"
	"time"
)

type Kind string

const (
	KindProgress Kind = "progress"
	KindPartial  Kind = "partial"
	KindResult   Kind = "result"
	KindError    Kind = "error"
	KindCancel   Kind = "cancel"
)

// Event is the generic envelope every local-engine stage emits.
// Message is product language: never a path, backend name, or file format.
type Event struct {
	Kind    Kind           `json:"kind"`
	Stage   string         `json:"stage"`
	Message string         `json:"message,omitempty"`
	Done    int64          `json:"done,omitempty"`
	Total   int64          `json:"total,omitempty"`
	ETA     time.Duration  `json:"eta_ns,omitempty"`
	Range   string         `json:"range,omitempty"`
	Err     string         `json:"error,omitempty"`
	Extra   map[string]any `json:"extra,omitempty"`
}

type Sink func(Event)

func (e Event) Line() string {
	if e.Message != "" {
		return e.Message
	}
	switch e.Kind {
	case KindError:
		if e.Err != "" {
			return e.Err
		}
		return "semantic search failed"
	case KindCancel:
		return "semantic search cancelled"
	default:
		return e.Stage
	}
}

func Progress(stage, message string, done, total int64, eta time.Duration) Event {
	return Event{Kind: KindProgress, Stage: stage, Message: message, Done: done, Total: total, ETA: eta}
}

func Partial(stage, message, timeRange string, done, total int64, eta time.Duration) Event {
	return Event{Kind: KindPartial, Stage: stage, Message: message, Range: timeRange, Done: done, Total: total, ETA: eta}
}

func Result(stage, message string) Event {
	return Event{Kind: KindResult, Stage: stage, Message: message}
}

func Error(stage, message string) Event {
	return Event{Kind: KindError, Stage: stage, Message: message, Err: message}
}

func Cancel(stage, message string) Event {
	return Event{Kind: KindCancel, Stage: stage, Message: message}
}

func Percent(done, total int64) int {
	if total <= 0 {
		return 0
	}
	if done >= total {
		return 100
	}
	return int((done * 100) / total)
}

func FormatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
