package search

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
	Oversample     int
	RRFK           int
	MinVectorScore float64
	MaxRareTerms   int
	MaxDFRatio     float64
	ParallelLegs   bool
	Templates      TemplateMode
	TemplateList   []string
	Top            int
}

// Overlay is one request's explicit knob writes. A nil pointer leaves the
// value that config or the built-in default already chose.
type Overlay struct {
	Oversample     *int
	RRFK           *int
	MinVectorScore *float64
	MaxRareTerms   *int
	ParallelLegs   *bool
	NoTemplates    bool
}

// DefaultSettings is today's baked-in hybrid: oversample 100, RRF k=60,
// rarity keep 5, cosine floor 0.35, sequential legs, built-in templates.
func DefaultSettings() Settings {
	return Settings{
		Oversample:     HybridOversample,
		RRFK:           RRFK,
		MinVectorScore: MinVectorScore,
		MaxRareTerms:   MaxRareTerms,
		MaxDFRatio:     MaxDFRatio,
		Templates:      TemplatesDefault,
		Top:            DefaultTop,
	}
}

// WithDefaults fills any non-positive numeric knob with the baked-in value.
// Template mode and ParallelLegs stay as written, including an explicit off.
func (s Settings) WithDefaults() Settings {
	if s.Oversample <= 0 {
		s.Oversample = HybridOversample
	}
	if s.RRFK <= 0 {
		s.RRFK = RRFK
	}
	if s.MinVectorScore <= 0 {
		s.MinVectorScore = MinVectorScore
	}
	if s.MaxRareTerms <= 0 {
		s.MaxRareTerms = MaxRareTerms
	}
	if s.MaxDFRatio <= 0 {
		s.MaxDFRatio = MaxDFRatio
	}
	if s.Top <= 0 {
		s.Top = DefaultTop
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
	if overlay.RRFK != nil {
		s.RRFK = *overlay.RRFK
	}
	if overlay.MinVectorScore != nil {
		s.MinVectorScore = *overlay.MinVectorScore
	}
	if overlay.MaxRareTerms != nil {
		s.MaxRareTerms = *overlay.MaxRareTerms
	}
	if overlay.ParallelLegs != nil {
		s.ParallelLegs = *overlay.ParallelLegs
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
