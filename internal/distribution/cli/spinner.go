package cli

import (
	"fmt"
	"io"
	"strings"
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

const (
	spinnerShaping   = "🪨 shaping the search"
	spinnerSearching = "🪨 searching memory"
	spinnerComposing = "🪨 composing the answer"
)

type spinner struct {
	out     io.Writer
	label   string
	active  bool
	stop    chan struct{}
	done    chan struct{}
	started bool
	preview string
	space   bool
	mu      sync.Mutex
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
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
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
	s.mu.Lock()
	label, preview := s.label, s.preview
	s.mu.Unlock()
	glyph := spinnerFrames[frame%len(spinnerFrames)]
	if preview != "" {
		label += " · " + preview
	}
	fmt.Fprintf(s.out, "%s%s %s", clearLine, paint(s.out, ansiCyan, glyph), label)
}

func (s *spinner) phase(label string) {
	s.mu.Lock()
	s.label, s.preview, s.space = label, "", false
	s.mu.Unlock()
}

func (s *spinner) appendPreview(delta string) {
	s.mu.Lock()
	clean := strings.Join(strings.Fields(delta), " ")
	if clean != "" && s.preview != "" && s.space {
		s.preview += " "
	}
	s.preview += clean
	s.space = len(delta) > 0 && strings.ContainsAny(delta[len(delta)-1:], " \t\r\n")
	runes := []rune(s.preview)
	if len(runes) > 80 {
		s.preview = "…" + string(runes[len(runes)-79:])
	}
	s.mu.Unlock()
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
		s.mu.Lock()
		started := s.started
		s.mu.Unlock()
		if started {
			fmt.Fprint(s.out, clearLine)
		}
	})
}
