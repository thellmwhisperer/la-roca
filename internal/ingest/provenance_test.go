package ingest

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

// Provenance is the answer to "which model answered, how much did it think, what
// did it cost". It is filled from what each source itself recorded, so the test
// is one row per source: the column is filled where the artefact states it and
// NULL where the artefact says nothing, and a source that stops filling one is
// visible here and not in a query three weeks later.

type provenanceRow struct {
	model, provider             sql.NullString
	tokensIn, tokensOut, reason sql.NullInt64
	cost                        sql.NullFloat64
}

type provenanceExpectation struct {
	session                     string
	model, provider             string
	tokensIn, tokensOut, reason int
	cost                        float64
	// counted names the numeric columns the source does state, so an absent
	// one is asserted absent instead of compared against a zero.
	counted string
}

var recordedProvenance = []provenanceExpectation{
	// The three prompt tiers of a Claude transcript are one number, and the
	// runtime separates no reasoning tokens out of the answer.
	{session: fixtureSessionID, model: "fixture-claude-model",
		tokensIn: 35, tokensOut: 7, counted: "in out"},
	{session: "child-1", model: "fixture-subagent-model",
		tokensIn: 4, tokensOut: 2, counted: "in out"},
	{session: "cowork-1", model: "fixture-cowork-model",
		tokensIn: 6, tokensOut: 1, counted: "in out"},
	// A rollout counts the reasoning tokens apart and names who served it.
	{session: "codex-thread-1", model: "fixture-codex-model", provider: "fixture-provider",
		tokensIn: 31, tokensOut: 9, reason: 4, counted: "in out reasoning"},
	// Pi and OpenCode are the two that also price the turn.
	{session: "pi:pi-1", model: "fixture-pi-model", provider: "fixture-pi-provider",
		tokensIn: 15, tokensOut: 5, reason: 2, cost: 0.25, counted: "in out reasoning cost"},
	{session: "opencode:oc1", model: "fixture-opencode-model", provider: "fixture-opencode-provider",
		tokensIn: 43, tokensOut: 11, reason: 6, cost: 0.5, counted: "in out reasoning cost"},
	// Hermes measures a whole session and never a turn: the turn carries who
	// answered and no invented split of the totals.
	{session: "h1", model: "test-model", provider: "fixture-hermes-provider"},
	// The web export is the signal-poor source, and every column stays NULL.
	{session: "web-fixture-1"},
}

// readProvenance keys on the session and not on the agent: the synthetic world
// files two sessions under the Claude family, and the desktop metadata renames
// the transcript's own to the runtime that owns it.
func readProvenance(t *testing.T, db *sql.DB, session string) provenanceRow {
	t.Helper()
	var got provenanceRow
	err := db.QueryRow(`
		SELECT model, provider, tokens_in, tokens_out, tokens_reasoning, cost_usd
		FROM exchanges WHERE session_id = ? ORDER BY exchange_number LIMIT 1`, session).
		Scan(&got.model, &got.provider, &got.tokensIn, &got.tokensOut, &got.reason, &got.cost)
	if err != nil {
		t.Fatalf("read the provenance of %s: %v", session, err)
	}
	return got
}

func TestEverySourceFillsTheProvenanceItRecords(t *testing.T) {
	world := newWorld(t)
	db := rocaDatabase(t)
	if _, err := Run(context.Background(), db, registry(t), Options{Roots: world.roots()}); err != nil {
		t.Fatalf("run: %v", err)
	}

	assertRecordedProvenance(t, db.SQL())
}

// assertRecordedProvenance checks what each source of the synthetic world
// states about its first exchange. The zero value of a field is "this source
// states nothing": an invented zero is worse than an empty column because no
// query can tell it from a measurement.
func assertRecordedProvenance(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, want := range recordedProvenance {
		got := readProvenance(t, db, want.session)
		if got.model.String != want.model || got.provider.String != want.provider {
			t.Errorf("%s: model/provider = %q/%q, want %q/%q",
				want.session, got.model.String, got.provider.String, want.model, want.provider)
		}
		for _, counter := range []struct {
			name string
			got  sql.NullInt64
			want int
		}{
			{"in", got.tokensIn, want.tokensIn},
			{"out", got.tokensOut, want.tokensOut},
			{"reasoning", got.reason, want.reason},
		} {
			stated := strings.Contains(want.counted, counter.name)
			if counter.got.Valid != stated || (stated && int(counter.got.Int64) != counter.want) {
				t.Errorf("%s: tokens %s = %v (stated=%t), want %d (stated=%t)",
					want.session, counter.name, counter.got.Int64, counter.got.Valid, counter.want, stated)
			}
		}
		priced := strings.Contains(want.counted, "cost")
		if got.cost.Valid != priced || (priced && got.cost.Float64 != want.cost) {
			t.Errorf("%s: cost = %v (stated=%t), want %v (stated=%t)",
				want.session, got.cost.Float64, got.cost.Valid, want.cost, priced)
		}
	}
}

// The re-ingest that backfills. Historical parsers did not always assign the
// exchange number that today's parser does, so the fixture moves those rows to
// numbers the current parse will not produce. The content and timestamps are
// unchanged: they are the stable evidence that the old row is the same turn.
func TestAPlainReingestMatchesHistoricalNumbersWithoutDuplicating(t *testing.T) {
	_, db, ctx, options := seededWorld(t)

	before := tableSnapshot(t, db.SQL())
	// What v1.8.1 left behind: NULL provenance, parser-v3 watermarks, and rows
	// numbered by the older Claude, Codex, Cowork, subagent and Hermes readers.
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE exchanges SET model = NULL, provider = NULL,
			tokens_in = NULL, tokens_out = NULL, tokens_reasoning = NULL, cost_usd = NULL`)
		if err != nil {
			return err
		}
		for _, table := range []string{"exchanges", "thinking_blocks", "tool_uses"} {
			for _, offset := range []int{100, -99} {
				_, err = tx.ExecContext(ctx, `UPDATE `+table+` SET exchange_number = exchange_number + ?
					WHERE session_id IN (?, ?, ?, ?, ?)`, offset, fixtureSessionID, "child-1", "cowork-1",
					"codex-thread-1", "h1")
				if err != nil {
					return err
				}
			}
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE ingest_file_state SET fingerprint = replace(fingerprint, '-v4', '-v3')
			 WHERE instr(fingerprint, ':parser:') > 0`)
		return err
	}); err != nil {
		t.Fatalf("age the corpus: %v", err)
	}

	if _, err := Run(ctx, db, registry(t), options); err != nil {
		t.Fatalf("second run: %v", err)
	}

	if after := tableSnapshot(t, db.SQL()); after != before {
		t.Errorf("the backfill changed the row counts: %+v, want %+v", after, before)
	}
	assertRecordedProvenance(t, db.SQL())
	result, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("idempotent run: %v", err)
	}
	if result.FilesRead != 0 || result.Delta != (Tables{}) {
		t.Errorf("idempotent run read or wrote records: files=%d delta=%+v",
			result.FilesRead, result.Delta)
	}
}

// A value that already answered a query is never rewritten, not even by a source
// that has since changed its mind: the backfill only fills what is NULL.
func TestTheBackfillNeverOverwritesProvenanceThatLanded(t *testing.T) {
	_, db, ctx, options := seededWorld(t)
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE exchanges SET model = 'landed-first'`)
		return err
	}); err != nil {
		t.Fatalf("pin the models: %v", err)
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE ingest_file_state SET fingerprint = 'stale'`)
		return err
	}); err != nil {
		t.Fatalf("age the watermarks: %v", err)
	}
	if _, err := Run(ctx, db, registry(t), options); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var overwritten int
	err := db.SQL().QueryRow(`SELECT COUNT(*) FROM exchanges WHERE model <> 'landed-first'`).
		Scan(&overwritten)
	if err != nil {
		t.Fatalf("count the rewritten models: %v", err)
	}
	if overwritten != 0 {
		t.Errorf("%d exchanges had their model rewritten", overwritten)
	}
}

func TestBackfillRefusesMismatchedAndAmbiguousContent(t *testing.T) {
	db := rocaDatabase(t)
	ctx := context.Background()
	write := func(exchanges []parsers.Exchange) Counts {
		t.Helper()
		var counts Counts
		err := db.Write(ctx, func(tx *sql.Tx) error {
			var err error
			counts, err = WriteRecords(ctx, tx, registry(t), parsers.Records{Sessions: []parsers.Session{{
				ID: "synthetic-anchor-safety", Exchanges: exchanges,
			}}})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return counts
	}
	write([]parsers.Exchange{
		{Number: 1, HumanText: "stored turn", AgentText: "stored answer"},
		{Number: 7, HumanText: "repeated turn", AgentText: "repeated answer"},
		{Number: 8, HumanText: "repeated turn", AgentText: "repeated answer"},
	})
	model := parsers.Provenance{Model: "must-not-land"}
	unsafe := []parsers.Exchange{
		{Number: 1, HumanText: "different turn", AgentText: "different answer", Provenance: model},
		{Number: 2, HumanText: "repeated turn", AgentText: "repeated answer", Provenance: model},
	}
	for range 2 {
		if counts := write(unsafe); counts.Exchanges != 0 {
			t.Fatalf("unsafe replay inserted exchanges: %+v", counts)
		}
	}
	var rows, enriched int
	if err := db.SQL().QueryRow(`SELECT COUNT(*), COUNT(model) FROM exchanges
		WHERE session_id = 'synthetic-anchor-safety'`).Scan(&rows, &enriched); err != nil {
		t.Fatal(err)
	}
	if rows != 3 || enriched != 0 {
		t.Fatalf("unsafe replay left rows/enriched = %d/%d, want 3/0", rows, enriched)
	}
}

func TestAReingestEnrichesMissingAnswerAndThinkingOnlyOnce(t *testing.T) {
	db := rocaDatabase(t)
	ctx := context.Background()
	write := func(answer, depth string) Counts {
		t.Helper()
		var counts Counts
		exchange := parsers.Exchange{
			Number: 1, HumanText: "recover the answer", AgentText: answer,
		}
		if depth != "" {
			exchange.Thinking = []parsers.Thinking{{
				Text: "verify the recovered answer", Depth: depth, WordCount: 4,
			}}
		}
		err := db.Write(ctx, func(tx *sql.Tx) error {
			var err error
			counts, err = WriteRecords(ctx, tx, registry(t), parsers.Records{Sessions: []parsers.Session{{
				ID: "synthetic-codex-backfill", SourceAgent: "codex",
				Exchanges: []parsers.Exchange{exchange},
			}}})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return counts
	}

	write("", "")
	if counts := write("recovered answer", "first"); counts.ThinkingBlocks != 1 {
		t.Fatalf("enrichment counts = %+v", counts)
	}
	if counts := write("replacement answer", "replacement"); counts.ThinkingBlocks != 0 {
		t.Fatalf("idempotent enrichment counts = %+v", counts)
	}
	var answer, depth string
	err := db.SQL().QueryRow(`
		SELECT e.agent_text, t.depth FROM exchanges e
		JOIN thinking_blocks t USING (session_id, exchange_number)
		WHERE e.session_id = 'synthetic-codex-backfill'`).Scan(&answer, &depth)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "recovered answer" || depth != "first" ||
		countRows(t, db.SQL(), "thinking_blocks WHERE session_id = 'synthetic-codex-backfill'") != 1 {
		t.Fatalf("enriched answer/thinking = %q/%q", answer, depth)
	}
}

func TestOpenCodePartialUsageDoesNotInventInputTokens(t *testing.T) {
	output := 7.0
	got := openCodeProvenance([]openCodeRow{{message: openCodeMessage{
		Tokens: &openCodeTokens{Output: &output},
	}}})
	if got.TokensIn != nil || got.TokensOut == nil || *got.TokensOut != 7 {
		t.Fatalf("partial OpenCode usage = input:%v output:%v", got.TokensIn, got.TokensOut)
	}
}

func tableSnapshot(t *testing.T, db *sql.DB) Tables {
	t.Helper()
	counts, err := tableCounts(context.Background(), db)
	if err != nil {
		t.Fatalf("count the tables: %v", err)
	}
	return counts
}
