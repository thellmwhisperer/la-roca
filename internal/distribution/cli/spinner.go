package cli

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// A spinner is the one piece of motion La Roca shows: it tells an operator a
// query is running instead of looking frozen, and it tells nobody else. It
// starts on the error stream of an interactive terminal only, it waits out a
// grace window before it draws (the local route is often a few milliseconds and
// a flicker is worse than nothing), and finish clears its line so the result
// prints on a clean row. Under --json it is never started.

var (
	spinnerGrace = 200 * time.Millisecond
	spinnerTick  = 70 * time.Millisecond
)

// clearLine returns the cursor to the first column and erases the row, so the
// result prints where the spinner was without leaving a half-glyph behind.
const clearLine = "\r\x1b[2K"

// spinnerFrames is the braille cycle every modern tool settles on: one glyph
// wide, legible at a glance and quiet on a slow terminal.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerLabel carries the rock's voice in the register the rest of the
// narration uses: terse, lowercase, present tense.
const spinnerLabel = "🪨 searching memory"

type spinner struct {
	out     io.Writer
	label   string
	active  bool
	stop    chan struct{}
	done    chan struct{}
	started bool
	// once keeps finish idempotent. The contract below promises it is safe to
	// defer, which invites a caller to both defer it and call it on the success
	// path; the second close of stop panicked instead.
	once sync.Once
}

// startSpinner decides whether to animate and returns a spinner that is always
// safe to finish. A non-terminal or a --json call gets an inert spinner whose
// finish is a no-op, so the caller never has to branch.
func startSpinner(env *cliEnv, label string) *spinner {
	return newSpinner(env.errOut, label, !env.json && termAware(env.errOut))
}

// newSpinner owns the activation decision the caller already made, so the draw
// and clear logic is testable on a plain buffer without faking a terminal.
func newSpinner(out io.Writer, label string, active bool) *spinner {
	s := &spinner{out: out, label: label, active: active}
	if !active {
		return s
	}
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.run()
	return s
}

func (s *spinner) run() {
	defer close(s.done)
	select {
	case <-s.stop:
		return
	case <-time.After(spinnerGrace):
	}
	s.started = true
	ticker := time.NewTicker(spinnerTick)
	defer ticker.Stop()
	for frame := 0; ; frame++ {
		s.draw(frame)
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
	}
}

// draw paints one frame: clear the row, then the spinning glyph and the label.
// The glyph is coloured on a terminal that allows it and plain otherwise, so a
// stream that receives a draw without colour still reads cleanly.
func (s *spinner) draw(frame int) {
	glyph := spinnerFrames[frame%len(spinnerFrames)]
	fmt.Fprintf(s.out, "%s%s %s", clearLine, paint(s.out, ansiCyan, glyph), s.label)
}

// finish stops the spinner, joins its goroutine and, if it ever drew, clears the
// row. Safe on an inert spinner, safe to defer, and safe to call twice.
func (s *spinner) finish() {
	if !s.active {
		return
	}
	s.once.Do(func() {
		close(s.stop)
		<-s.done
		if s.started {
			fmt.Fprint(s.out, clearLine)
		}
	})
}
