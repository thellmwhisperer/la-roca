package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

func TestProseStreamWrapsWordsAcrossProviderChunks(t *testing.T) {
	var output bytes.Buffer
	stream := newProseStreamWriter(&output, 40)
	for _, delta := range []string{
		"Actual prose arrives in inconvenient chu",
		"nks but wraps at the terminal boundary without a sliding preview.",
	} {
		stream.append(delta)
	}
	stream.finish()

	got := output.String()
	if strings.ReplaceAll(got, "\n", " ") !=
		"Actual prose arrives in inconvenient chunks but wraps at the terminal boundary without a sliding preview. " {
		t.Fatalf("stream changed the prose:\n%q", got)
	}
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if width := runewidth.StringWidth(line); width >= 40 {
			t.Fatalf("streamed line is %d cells in a 40-column terminal: %q", width, line)
		}
	}
}

func TestProseStreamPreservesParagraphs(t *testing.T) {
	var output bytes.Buffer
	stream := newProseStreamWriter(&output, 80)
	stream.append("First paragraph.\n\n- second")
	stream.append(" paragraph")
	stream.finish()

	if got, want := output.String(), "First paragraph.\n\n- second paragraph\n"; got != want {
		t.Fatalf("streamed prose = %q, want %q", got, want)
	}
}

func TestLiveInterpretationRequiresFullTTYHumanOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		full bool
		json bool
		tty  bool
		want bool
	}{
		{name: "full tty", full: true, tty: true, want: true},
		{name: "default tty", tty: true},
		{name: "full pipe", full: true},
		{name: "full json tty", full: true, json: true, tty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := wantsLiveInterpretation(tc.full, tc.json, tc.tty)
			if got != tc.want {
				t.Fatalf("wants live interpretation = %v, want %v", got, tc.want)
			}
		})
	}
}
