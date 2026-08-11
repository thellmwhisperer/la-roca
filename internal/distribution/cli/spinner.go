package cli

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mattn/go-runewidth"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
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
	width   int
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
	return newSpinnerAtWidth(env.errOut, label, !env.json && termAware(env.errOut),
		detectedTerminalWidth(env.errOut))
}

// newSpinner owns the activation decision the caller already made, so the draw
// and clear logic is testable on a plain buffer without faking a terminal.
func newSpinner(out io.Writer, label string, active bool) *spinner {
	return newSpinnerAtWidth(out, label, active, fallbackTerminalWidth)
}

func newSpinnerAtWidth(out io.Writer, label string, active bool, width int) *spinner {
	s := &spinner{out: out, label: label, active: active, width: max(1, width)}
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
	label, width := s.label, s.width
	s.mu.Unlock()
	glyph := spinnerFrames[frame%len(spinnerFrames)]
	line := statusLine(glyph, label, width)
	painted := strings.Replace(line, glyph, paint(s.out, ansiCyan, glyph), 1)
	fmt.Fprint(s.out, clearLine, painted)
}

func statusLine(glyph, label string, width int) string {
	limit := max(1, width-1)
	base := glyph + " " + label
	if runewidth.StringWidth(base) > limit {
		return runewidth.Truncate(base, limit, "…")
	}
	return base
}

func (s *spinner) phase(label string) {
	s.mu.Lock()
	s.label = label
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

// liveInterpretation owns the TTY-only transition from the phase line to the
// provider's real prose. Pipes, JSON and buffered providers never activate it.
type liveInterpretation struct {
	env       *cliEnv
	spin      *spinner
	writer    *proseStreamWriter
	requested bool
	native    bool
	wrote     bool
	dbPath    string
	result    service.QueryResult
	once      sync.Once
}

func newLiveInterpretation(env *cliEnv, spin *spinner, full bool, dbPath string) *liveInterpretation {
	requested := wantsLiveInterpretation(full, env.json, termAware(env.out))
	return &liveInterpretation{
		env: env, spin: spin, requested: requested, dbPath: dbPath,
		writer: newProseStreamWriter(env.out, terminalWidth(env.out)),
	}
}

func wantsLiveInterpretation(full, json, tty bool) bool { return full && !json && tty }

func (l *liveInterpretation) start(native bool, result service.QueryResult) {
	l.native, l.result = l.requested && native, result
}

func (l *liveInterpretation) append(delta string) {
	if !l.native || delta == "" {
		return
	}
	l.once.Do(func() {
		l.spin.finish()
		fmt.Fprintf(l.env.out, "database: %s\n%s\n", l.dbPath, axi.QueryPreamble(l.result))
	})
	l.wrote = true
	l.writer.append(delta)
}

// finish returns true when the live prose is already the complete human answer
// and the buffered renderer must not print a second copy.
func (l *liveInterpretation) finish(answer queryAnswer) bool {
	if !l.native || !l.wrote {
		return false
	}
	l.writer.finish()
	if answer.interpretErr != nil {
		l.env.print("%s", interpretationFallback(answer.interpretErr))
		l.env.print("%s", rowOutput(answer.result.Columns, answer.result.Rows, answer.result.Question))
	}
	return true
}

// proseStreamWriter preserves provider text while wrapping complete words one
// column inside the detected terminal edge. Holding only the current word
// makes arbitrary provider chunk boundaries invisible to the reader.
type proseStreamWriter struct {
	out     io.Writer
	width   int
	column  int
	pending strings.Builder
	space   strings.Builder
}

func newProseStreamWriter(out io.Writer, width int) *proseStreamWriter {
	return &proseStreamWriter{out: out, width: max(1, saneTerminalWidth(width)-1)}
}

func (w *proseStreamWriter) append(delta string) {
	for _, r := range delta {
		if !unicode.IsSpace(r) {
			w.pending.WriteRune(r)
			continue
		}
		w.flushWord()
		if r == '\n' || r == '\r' {
			w.space.Reset()
			fmt.Fprint(w.out, string(r))
			w.column = 0
			continue
		}
		w.space.WriteRune(r)
	}
}

func (w *proseStreamWriter) flushWord() {
	word := w.pending.String()
	if word == "" {
		return
	}
	wordWidth := runewidth.StringWidth(word)
	space := w.space.String()
	spaceWidth := runewidth.StringWidth(space)
	if w.column > 0 && w.column+spaceWidth+wordWidth > w.width {
		fmt.Fprint(w.out, "\n")
		w.column = 0
		space = ""
		spaceWidth = 0
	}
	if w.column > 0 && space != "" {
		fmt.Fprint(w.out, space)
		w.column += spaceWidth
	}
	for _, r := range word {
		cell := runewidth.RuneWidth(r)
		if w.column > 0 && w.column+cell > w.width {
			fmt.Fprint(w.out, "\n")
			w.column = 0
		}
		fmt.Fprint(w.out, string(r))
		w.column += cell
	}
	w.pending.Reset()
	w.space.Reset()
}

func (w *proseStreamWriter) finish() {
	w.flushWord()
	fmt.Fprint(w.out, "\n")
}
