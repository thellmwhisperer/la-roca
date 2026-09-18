package search

import (
	"fmt"
	"math"
)

// TemplateMode is how the vector leg expands the raw question.
type TemplateMode int

const (
	// TemplatesDefault embeds the raw question plus the plugin's built-in
	// question wrappers.
	TemplatesDefault TemplateMode = iota
	// TemplatesOff embeds only the raw question.
	TemplatesOff
	// TemplatesCustom embeds the raw question plus TemplateList.
	TemplatesCustom
)

// Settings are the hybrid retrieval knobs. Zero values in Overlay mean "leave
// the current setting"; Settings itself is always a complete, resolved set.
type Settings struct {
	Oversample        int
	RRFK              int
	MinVectorScore    float64
	MinVectorScoreSet bool
	MaxRareTerms      int
	ParallelLegs      bool
	ParallelLegsSet   bool
	Templates         TemplateMode
	TemplateList      []string
}

// Validate checks the operator-controlled numeric retrieval knobs.
func (s Settings) Validate() error {
	switch {
	case s.Oversample < 1 || s.Oversample > MaxOversample:
		return fmt.Errorf("oversample must be between 1 and %d", MaxOversample)
	case s.RRFK < 1:
		return fmt.Errorf("rrf-k must be 1 or greater")
	case uint64(s.RRFK) > MaxRRFK:
		return fmt.Errorf("rrf-k must be no greater than %d", MaxRRFK)
	case s.MinVectorScore < 0 || math.IsNaN(s.MinVectorScore) || math.IsInf(s.MinVectorScore, 0):
		return fmt.Errorf("min-vector-score must be a finite non-negative number")
	case s.MaxRareTerms < 1:
		return fmt.Errorf("max-rare-terms must be 1 or greater")
	default:
		return nil
	}
}

// Overlay is one request's explicit knob writes. A nil pointer leaves the
// value that config or the built-in default already chose.
type Overlay struct {
	Oversample  *int
	NoTemplates bool
}

// DefaultSettings is today's baked-in hybrid: oversample 10, RRF k=60,
// rarity keep 5, cosine floor 0.35, parallel legs, raw-question embeddings.
func DefaultSettings() Settings {
	return Settings{
		Oversample:      HybridOversample,
		RRFK:            RRFK,
		MinVectorScore:  MinVectorScore,
		MaxRareTerms:    MaxRareTerms,
		ParallelLegs:    true,
		ParallelLegsSet: true,
		Templates:       TemplatesOff,
	}
}

// WithDefaults fills unset numeric knobs with the baked-in value. Template
// mode stays as written, including an explicit on. Unset ParallelLegs follows
// the baked-in default; ParallelLegsSet keeps an explicit off.
func (s Settings) WithDefaults() Settings {
	if s.Oversample <= 0 {
		s.Oversample = HybridOversample
	}
	if s.RRFK <= 0 {
		s.RRFK = RRFK
	}
	if !s.MinVectorScoreSet && s.MinVectorScore <= 0 {
		s.MinVectorScore = MinVectorScore
	}
	if s.MaxRareTerms <= 0 {
		s.MaxRareTerms = MaxRareTerms
	}
	if !s.ParallelLegsSet {
		s.ParallelLegs = true
		s.ParallelLegsSet = true
	}
	if s.Templates == TemplatesCustom && len(s.TemplateList) == 0 {
		s.Templates = TemplatesOff
	}
	return s
}

// Apply writes overlay values on top of s. Flag occupancy is the caller's
// job: only set pointers for knobs the operator named.
func (s Settings) Apply(overlay Overlay) Settings {
	if overlay.Oversample != nil {
		s.Oversample = *overlay.Oversample
	}
	if overlay.NoTemplates {
		s.Templates = TemplatesOff
		s.TemplateList = nil
	}
	return s
}

// ExpandTemplates reports whether the vector leg should wrap the question.
func (s Settings) ExpandTemplates() bool {
	return s.Templates != TemplatesOff
}
