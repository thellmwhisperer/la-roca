// Package corpuswriter writes normalized conversations into a La Roca corpus.
//
// Write expects a transaction whose database already has the corpus schema,
// full-text triggers, and exact-payload guards installed. It owns the insert,
// enrichment, deduplication, and FTS-triggering write path for sessions and
// their exchanges, thinking blocks, and tool uses.
package corpuswriter
