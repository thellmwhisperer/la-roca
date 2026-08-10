// Package hooks is the session-lifecycle half of La Roca: what a fresh agent
// session is handed, under a measured budget, and how a runtime declares the
// commands that ask for it.
//
// The law the whole package obeys: a hook is a subprocess on the critical path
// of somebody's session. It runs one CLI command and reads its standard output.
// It does not open the database itself and it does not speak MCP, because both
// would put a second way of reaching the kernel next to the one the product
// already has.
package hooks

import (
	"strconv"
	"strings"
)

// The injection budget's constants, the laboratory's, measured and not guessed.
const (
	// DefaultMaxChars is what a session receives when nobody says otherwise.
	DefaultMaxChars = 12000
	// MinSectionChars is the floor under which a section is not worth
	// injecting: less than this is a fragment that reads like a whole thing
	// saying something else.
	MinSectionChars = 160

	sectionSeparator = "\n\n"
	truncationMark   = "\n[trimmed]"
)

// EnvMaxChars and ConfigMaxChars are where the limit is declared, in increasing
// order of precedence when read from left to right.
const (
	ConfigMaxChars = "hooks_max_chars"
	EnvMaxChars    = "ROCA_SESSIONSTART_MAX_CHARS"
)

// What the budget did to one section.
const (
	StateFull    = "full"
	StateTrimmed = "trimmed"
	StateDropped = "dropped"
)

// Section is one named block of candidate context with a heading of its own.
type Section struct {
	Name  string
	Title string
	Body  string
}

func (s Section) rendered() string {
	body := strings.TrimSpace(s.Body)
	switch {
	case body == "":
		return ""
	case s.Title == "":
		return body
	default:
		return s.Title + "\n" + body
	}
}

// SectionReport is what happened to one section, in characters.
type SectionReport struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	Chars       int    `json:"chars"`
	SourceChars int    `json:"source_chars"`
}

// BudgetReport is the measurable outcome of one budgeted render. It is what makes the
// budget a contract instead of a habit: a caller can assert on these numbers.
type BudgetReport struct {
	Limit        int             `json:"limit"`
	Used         int             `json:"used"`
	PointerChars int             `json:"pointer_chars"`
	Sections     []SectionReport `json:"sections"`
}

// Incomplete names the sections that did not go in whole.
func (r BudgetReport) Incomplete() []string {
	var names []string
	for _, section := range r.Sections {
		if section.State != StateFull {
			names = append(names, section.Name)
		}
	}
	return names
}

// ResolveLimit reads the injection cap: the environment, then the configuration
// file, then the default. A value nobody can read as a number falls back to the
// default, because injecting the usual amount is safer than injecting nothing
// over a typo.
func ResolveLimit(fromEnvironment, fromConfig string) int {
	for _, declared := range []string{fromEnvironment, fromConfig} {
		if strings.TrimSpace(declared) == "" {
			continue
		}
		value, err := strconv.Atoi(strings.TrimSpace(declared))
		if err != nil {
			return DefaultMaxChars
		}
		return max(0, value)
	}
	return DefaultMaxChars
}

// Render lays the sections out under a hard character cap and reports what it
// cost.
//
// The rule is fair-share water filling: when everything does not fit, each
// section gets an equal slice of what is left, a section needing less than its
// slice gives the surplus back, and a section that cannot even hold its floor is
// dropped whole. That is what stops one oversized handoff from starving the
// pills.
func Render(sections []Section, limit int) (string, BudgetReport) {
	type candidate struct {
		section Section
		text    string
	}
	var candidates []candidate
	for _, section := range sections {
		if text := section.rendered(); text != "" {
			candidates = append(candidates, candidate{section, text})
		}
	}
	if len(candidates) == 0 || limit <= 0 {
		return "", BudgetReport{Limit: limit}
	}

	names := make([]string, len(candidates))
	sizes := make([]int, len(candidates))
	for i, c := range candidates {
		names[i], sizes[i] = c.section.Name, len(c.text)
	}
	separators := len(sectionSeparator) * (len(candidates) - 1)
	reserve := len(pointerText(limit, names)) + len(sectionSeparator)
	withPointer := limit-reserve-separators >= MinSectionChars
	available := limit - separators
	if withPointer {
		available -= reserve
	}
	allocation := allocate(sizes, max(available, 0))

	var parts []string
	report := BudgetReport{Limit: limit}
	for i, c := range candidates {
		kept := fit(c.text, allocation[i])
		report.Sections = append(report.Sections, SectionReport{
			Name:        c.section.Name,
			State:       stateOf(len(kept), sizes[i]),
			Chars:       len(kept),
			SourceChars: sizes[i],
		})
		if kept != "" {
			parts = append(parts, kept)
		}
	}
	if len(parts) == 0 {
		return "", report
	}

	text := strings.Join(parts, sectionSeparator)
	if withPointer {
		pointer := pointerText(limit, report.Incomplete())
		text += sectionSeparator + pointer
		report.PointerChars = len(pointer)
	}
	report.Used = len(text)
	return text, report
}

// pointerText is the closing line that sends the agent back to La Roca for
// whatever did not fit. Nothing is lost: it is left where it lives.
func pointerText(limit int, incomplete []string) string {
	detail := ""
	if len(incomplete) > 0 {
		detail = " (trimmed to fit: " + strings.Join(incomplete, ", ") + ")"
	}
	return "[roca] Injected context budget: " + strconv.Itoa(limit) + " chars" + detail +
		". The rest stays in La Roca: dig with `roca query` or `roca hook handoff` " +
		"instead of deriving it again."
}

// allocate is the fair share: equal slices, and the surplus of whoever needs
// less goes back into the pool for the others.
func allocate(sizes []int, available int) []int {
	allocation := make([]int, len(sizes))
	pending := make([]int, len(sizes))
	for i := range sizes {
		pending[i] = i
	}

	for len(pending) > 0 {
		share := available / len(pending)
		if share < MinSectionChars {
			break
		}
		var satisfied, rest []int
		for _, i := range pending {
			if sizes[i] <= share {
				satisfied = append(satisfied, i)
			} else {
				rest = append(rest, i)
			}
		}
		if len(satisfied) == 0 {
			for _, i := range pending {
				allocation[i] = share
			}
			return allocation
		}
		for _, i := range satisfied {
			allocation[i] = sizes[i]
			available -= sizes[i]
		}
		pending = rest
	}

	// What is left over is spent in declaration order, and whatever no longer
	// fits whole is dropped instead of cut into a fragment.
	for _, i := range pending {
		if available < MinSectionChars {
			continue
		}
		allocation[i] = min(sizes[i], available)
		available -= allocation[i]
	}
	return allocation
}

func fit(text string, allowed int) string {
	if allowed >= len(text) {
		return text
	}
	if allowed < MinSectionChars {
		return ""
	}
	return strings.TrimRight(text[:allowed-len(truncationMark)], " \t\n") + truncationMark
}

func stateOf(kept, size int) string {
	if kept == 0 {
		return StateDropped
	}
	if kept == size {
		return StateFull
	}
	return StateTrimmed
}
