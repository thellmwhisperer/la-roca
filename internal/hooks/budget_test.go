package hooks_test

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/hooks"
)

// Size discipline is a contract and not a habit: everything a lifecycle hook
// pushes into a fresh session competes with the user's own prompt for the same
// window. The rule the laboratory settled on is fair-share water filling, and
// these are the four things it promises.

func TestWhatFitsIsInjectedWholeWithThePointerAtTheEnd(t *testing.T) {
	text, report := hooks.Render([]hooks.Section{
		{Name: "pills", Title: "PILLS:", Body: "one pill"},
		{Name: "handoff", Title: "HANDOFF:", Body: "one handoff"},
	}, 12000)

	if !strings.Contains(text, "one pill") || !strings.Contains(text, "one handoff") {
		t.Errorf("something that fits was left out: %q", text)
	}
	for _, section := range report.Sections {
		if section.State != hooks.StateFull {
			t.Errorf("section %q = %q, want %q", section.Name, section.State, hooks.StateFull)
		}
	}
	// Whatever did not fit is not lost: the block always ends by pointing back
	// at La Roca, so the agent digs on demand instead of being force-fed.
	if report.PointerChars == 0 {
		t.Error("the block does not point back at La Roca")
	}
	if report.Used != len(text) {
		t.Errorf("used = %d, want the %d characters really rendered", report.Used, len(text))
	}
	if report.Used > report.Limit {
		t.Errorf("used = %d over a limit of %d", report.Used, report.Limit)
	}
}

// The promise that matters: one oversized handoff cannot starve the pills, and
// the other way round.
func TestOneOversizedSectionDoesNotStarveTheOther(t *testing.T) {
	text, report := hooks.Render([]hooks.Section{
		{Name: "pills", Title: "PILLS:", Body: "a short pill"},
		{Name: "handoff", Title: "HANDOFF:", Body: strings.Repeat("x", 20000)},
	}, 2000)

	if !strings.Contains(text, "a short pill") {
		t.Error("the short section was starved by the long one")
	}
	if len(text) > 2000 {
		t.Errorf("%d characters over a limit of 2000", len(text))
	}
	states := statesOf(report)
	if states["pills"] != hooks.StateFull {
		t.Errorf("pills = %q, want %q: it fitted in its share", states["pills"], hooks.StateFull)
	}
	if states["handoff"] != hooks.StateTrimmed {
		t.Errorf("handoff = %q, want %q", states["handoff"], hooks.StateTrimmed)
	}
	if !strings.Contains(text, "[trimmed]") {
		t.Error("what was cut is not declared cut")
	}
}

// A section that cannot even hold its floor is dropped whole. Half a handoff
// reads like a whole one that says something else.
func TestASectionThatCannotHoldItsFloorIsDroppedWholeAndNotCutIntoAFragment(t *testing.T) {
	text, report := hooks.Render([]hooks.Section{
		{Name: "pills", Title: "PILLS:", Body: strings.Repeat("p", 400)},
		{Name: "handoff", Title: "HANDOFF:", Body: strings.Repeat("h", 400)},
	}, 300)

	if len(text) > 300 {
		t.Errorf("%d characters over a limit of 300", len(text))
	}
	dropped := 0
	for _, section := range report.Sections {
		if section.State == hooks.StateDropped {
			dropped++
		}
		if section.State == hooks.StateTrimmed && section.Chars < hooks.MinSectionChars {
			t.Errorf("section %q kept %d characters, under the floor of %d",
				section.Name, section.Chars, hooks.MinSectionChars)
		}
	}
	if dropped == 0 {
		t.Error("nothing was dropped where nothing fitted")
	}
}

func TestTheReportNamesWhatWasNotInjectedWhole(t *testing.T) {
	text, report := hooks.Render([]hooks.Section{
		{Name: "pills", Title: "PILLS:", Body: "short"},
		{Name: "handoff", Title: "HANDOFF:", Body: strings.Repeat("x", 20000)},
	}, 2000)

	if got := report.Incomplete(); len(got) != 1 || got[0] != "handoff" {
		t.Errorf("incomplete = %v, want [handoff]", got)
	}
	if !strings.Contains(text, "handoff") {
		t.Error("the pointer does not name what was trimmed")
	}
}

func TestAnEmptySectionIsNotRenderedAtAll(t *testing.T) {
	text, report := hooks.Render([]hooks.Section{
		{Name: "pills", Title: "PILLS:", Body: "   "},
		{Name: "handoff", Title: "HANDOFF:", Body: "the only thing there is"},
	}, 12000)

	if strings.Contains(text, "PILLS:") {
		t.Errorf("an empty section was given a heading: %q", text)
	}
	if len(report.Sections) != 1 {
		t.Errorf("%d sections in the report, want only the one with content",
			len(report.Sections))
	}
}

func TestNothingToInjectRendersNothing(t *testing.T) {
	text, report := hooks.Render(nil, 12000)

	if text != "" {
		t.Errorf("text = %q, want nothing", text)
	}
	if report.Used != 0 {
		t.Errorf("used = %d, want 0", report.Used)
	}
}

// A limit of zero is an operator turning the injection off for a session, and
// it has to be obeyed to the character.
func TestALimitOfZeroInjectsNothing(t *testing.T) {
	text, _ := hooks.Render([]hooks.Section{
		{Name: "pills", Title: "PILLS:", Body: "something"},
	}, 0)

	if text != "" {
		t.Errorf("text = %q with a limit of 0", text)
	}
}

// The limit is resolved the way every other setting is: the environment, then
// the configuration file, then the default.
func TestTheLimitComesFromTheEnvironmentThenTheConfigThenTheDefault(t *testing.T) {
	cases := []struct {
		name            string
		env, configured string
		want            int
	}{
		{"nothing declared", "", "", hooks.DefaultMaxChars},
		{"only the config", "", "4000", 4000},
		{"the environment wins", "1500", "4000", 1500},
		{"a value nobody can read falls back", "not a number", "", hooks.DefaultMaxChars},
		{"turned off for this session", "0", "4000", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hooks.ResolveLimit(tc.env, tc.configured); got != tc.want {
				t.Errorf("limit = %d, want %d", got, tc.want)
			}
		})
	}
}

func statesOf(report hooks.BudgetReport) map[string]string {
	states := map[string]string{}
	for _, section := range report.Sections {
		states[section.Name] = section.State
	}
	return states
}
