// Package corpuswriter writes normalized conversations into a La Roca corpus.
//
// Write expects a transaction whose database already has the corpus schema,
// full-text triggers, and exact-payload guards installed. It routes sessions
// and their exchanges, thinking blocks, and tool uses through La Roca's shared
// insert, enrichment, deduplication, and FTS-triggering write path.
package corpuswriter
