package query

// Plan is the optional playground rescue envelope: which term, against which
// layer, with what cap. Core hybrid search does not compile this plan.
type Plan struct {
	Template string `json:"template"`
	Term     string `json:"term,omitempty"`
	Layer    string `json:"layer,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}
