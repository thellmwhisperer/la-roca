package service

// TerrainCount is one deterministic count calculated from returned rows.
type TerrainCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// Terrain is the factual map handed to the investigation interpreter. It is
// derived only from this run's rows and never from model knowledge.
type Terrain struct {
	RowCount      int            `json:"row_count"`
	Sources       []TerrainCount `json:"sources,omitempty"`
	DateClusters  []TerrainCount `json:"date_clusters,omitempty"`
	Terms         []TerrainCount `json:"co_occurring_terms,omitempty"`
	NegativeSpace []string       `json:"negative_space,omitempty"`
}
