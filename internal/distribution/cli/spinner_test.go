package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
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
