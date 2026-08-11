package cli

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

// A spinner is motion, and motion is for an interactive terminal only. These
// contracts hold the line the pinned output depends on: a piped command and a
// test buffer never receive a byte, and --json never starts one.

func fastSpinner(t *testing.T) {
	t.Helper()
	origGrace, origTick := spinnerGrace, spinnerTick
	spinnerGrace = 5 * time.Millisecond
	spinnerTick = 3 * time.Millisecond
	t.Cleanup(func() {
		spinnerGrace, spinnerTick = origGrace, origTick
	})
}

// A non-terminal errOut never spins, even after the grace window that would
// otherwise draw the first frame: this is the gate that keeps piped output clean.
func TestStartSpinnerIsInertWithoutATerminal(t *testing.T) {
	fastSpinner(t)
	env := &cliEnv{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	spin := startSpinner(env, "searching memory")
	time.Sleep(spinnerGrace + 3*spinnerTick)
	spin.finish()
	if got := env.errOut.(*bytes.Buffer).String(); got != "" {
		t.Errorf("a non-terminal errOut received spinner bytes:\n%q", got)
	}
}

// --json never spins: stdout carries only the envelope, and a spinner is noise
// a program does not read.
func TestStartSpinnerIsInertUnderJSON(t *testing.T) {
	env := &cliEnv{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, json: true}
	spin := startSpinner(env, "searching memory")
	spin.finish()
	if got := env.errOut.(*bytes.Buffer).String(); got != "" {
		t.Errorf("json mode received spinner bytes:\n%q", got)
	}
}

// The draw and clear logic, exercised directly on a buffer with the terminal
// gate bypassed: after the grace window it paints frames and the label, and
// finish clears the line so the result prints on a clean row.
func TestAnActiveSpinnerDrawsThenClears(t *testing.T) {
	fastSpinner(t)
	var buf bytes.Buffer
	spin := newSpinner(&buf, "searching memory", true)
	time.Sleep(spinnerGrace + 3*spinnerTick)
	spin.finish()
	got := buf.String()
	if !strings.Contains(got, "searching memory") {
		t.Errorf("the active spinner never painted its label:\n%q", got)
	}
	if !strings.ContainsAny(got, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Errorf("the active spinner never painted a braille frame:\n%q", got)
	}
	if !strings.HasSuffix(got, clearLine) {
		t.Errorf("finish did not clear the spinner line:\n%q", got)
	}
}

// finish joins the goroutine and is a no-op on an inert spinner, so a caller
// never has to branch and never leaves a half-drawn line behind.
func TestFinishIsSafeOnAnInertSpinner(t *testing.T) {
	env := &cliEnv{errOut: &bytes.Buffer{}}
	startSpinner(env, "x").finish()
}

// finish documents itself as safe to defer, and a caller that both defers it and
// calls it on the success path is exactly the pattern that wording invites. The
// second close of the stop channel panicked, so the promise was not kept.
func TestFinishIsSafeToCallTwice(t *testing.T) {
	fastSpinner(t)
	var buf bytes.Buffer
	spin := newSpinner(&buf, "searching memory", true)
	time.Sleep(spinnerGrace + 3*spinnerTick)

	spin.finish()
	spin.finish()

	if !strings.Contains(buf.String(), "searching memory") {
		t.Errorf("the spinner never painted its label:\n%q", buf.String())
	}
}

// syncBuffer lets the test read what the render goroutine has painted so far
// without racing it: fixed sleeps made this test flaky on slow runners, where
// a phase could be replaced before its first frame ever rendered.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitForFrame(t *testing.T, buf *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("phased spinner never painted %q:\n%q", want, buf.String())
}

func TestSpinnerNarratesOnlyTheBufferedPhases(t *testing.T) {
	fastSpinner(t)
	var buf syncBuffer
	spin := newSpinner(&buf, "shaping the search", true)
	waitForFrame(t, &buf, "shaping the search")
	spin.phase("searching memory")
	waitForFrame(t, &buf, "searching memory")
	spin.phase("composing the answer")
	waitForFrame(t, &buf, "composing the answer")
	spin.finish()
}

func TestPhaseStatusNeverWrapsOrWritesANewline(t *testing.T) {
	fastSpinner(t)
	var buf bytes.Buffer
	spin := newSpinnerAtWidth(&buf, spinnerComposing, true, 40)
	time.Sleep(spinnerGrace + 2*spinnerTick)
	spin.finish()

	for _, frame := range strings.Split(buf.String(), clearLine) {
		if frame == "" {
			continue
		}
		if strings.ContainsAny(frame, "\r\n") {
			t.Fatalf("status frame wrote a line break: %q", frame)
		}
		if width := runewidth.StringWidth(frame); width >= 40 {
			t.Fatalf("status frame is %d cells in a 40-column terminal: %q", width, frame)
		}
	}
}
