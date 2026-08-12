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
// exchange number or timestamp spelling that today's parser does, so the fixture
// recreates both differences. The underlying instants and content are stable
// evidence that the old row is the same turn.
func TestAPlainReingestMatchesHistoricalNumbersWithoutDuplicating(t *testing.T) {
	_, db, ctx, options := seededWorld(t)

	before := tableSnapshot(t, db.SQL())
	// What v1.8.3 left behind: NULL provenance, prior parser watermarks, and rows
	// numbered or timestamped by older readers.
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
		_, err = tx.ExecContext(ctx, `UPDATE exchanges SET exchange_number = NULL,
			human_timestamp = replace(human_timestamp, 'Z', '.000000+00:00'),
			agent_timestamp = replace(agent_timestamp, 'Z', '.000000+00:00')
			WHERE id = (SELECT id FROM exchanges WHERE session_id = ?
				ORDER BY exchange_number LIMIT 1)`, fixtureSessionID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE ingest_file_state SET fingerprint =
				replace(replace(replace(fingerprint, '-v7', '-v6'), '-v6', '-v5'),
				        'conversations-v4', 'conversations-v3')
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

func TestBackfillMatchesHistoricalAnchorsSafely(t *testing.T) {
	type storedRow struct {
		number                      int
		numberNull                  bool
		humanText, agentText, model string
	}
	provenance := func(model string) parsers.Provenance {
		return parsers.Provenance{Model: model}
	}
	tests := []struct {
		name                 string
		stored               []parsers.Exchange
		replay               []parsers.Exchange
		nullNumber           int
		wantInserted         int
		wantConflicts        int
		wantThinkingDiscards int
		want                 []storedRow
	}{
		{
			name: "equivalent timestamp spellings precede a repeated content anchor",
			stored: []parsers.Exchange{
				{Number: 1, HumanText: "same formatted turn", AgentText: "same formatted answer",
					HumanTimestamp: "2026-01-10T01:29:10.553000+00:00",
					AgentTimestamp: "2026-01-10T01:29:10.653000+00:00"},
				{Number: 7, HumanText: "same formatted turn", AgentText: "same formatted answer",
					HumanTimestamp: "2026-01-10T01:29:10.554000+00:00",
					AgentTimestamp: "2026-01-10T01:29:10.654000+00:00"},
			},
			replay: []parsers.Exchange{{
				Number: 1, HumanText: "same formatted turn", AgentText: "same formatted answer",
				HumanTimestamp: "2026-01-10T01:29:10.554Z",
				AgentTimestamp: "2026-01-10T01:29:10.654Z",
				Provenance:     provenance("normalized-timestamp-match"),
			}},
			want: []storedRow{
				{number: 1, humanText: "same formatted turn", agentText: "same formatted answer"},
				{number: 7, humanText: "same formatted turn", agentText: "same formatted answer",
					model: "normalized-timestamp-match"},
			},
		},
		{
			name: "timestamp anchors match a historical row without an exchange number",
			stored: []parsers.Exchange{{
				Number: 9, HumanText: "numberless turn", AgentText: "numberless answer",
				HumanTimestamp: "2026-01-10T01:29:10.554000+00:00",
				AgentTimestamp: "2026-01-10T01:29:10.654000+00:00",
			}},
			replay: []parsers.Exchange{{
				Number: 1, HumanText: "numberless turn", AgentText: "numberless answer",
				HumanTimestamp: "2026-01-10T01:29:10.554Z",
				AgentTimestamp: "2026-01-10T01:29:10.654Z",
				Provenance:     provenance("numberless-match"),
				Thinking:       []parsers.Thinking{{Text: "thinking without a row key"}},
			}},
			nullNumber:           9,
			wantThinkingDiscards: 1,
			want: []storedRow{{numberNull: true, humanText: "numberless turn",
				agentText: "numberless answer", model: "numberless-match"}},
		},
		{
			name: "a numbered original wins over its numberless duplicate",
			stored: []parsers.Exchange{
				{Number: 6, HumanText: "duplicated turn", AgentText: "duplicated answer",
					HumanTimestamp: "2026-03-18T13:07:01.053000+00:00",
					AgentTimestamp: "2026-03-18T13:07:02.053000+00:00"},
				{Number: 9, HumanText: "duplicated turn", AgentText: "duplicated answer",
					HumanTimestamp: "2026-03-18T13:07:01.053Z",
					AgentTimestamp: "2026-03-18T13:07:02.053Z"},
			},
			replay: []parsers.Exchange{{
				Number: 6, HumanText: "duplicated turn", AgentText: "duplicated answer",
				HumanTimestamp: "2026-03-18T13:07:01.053Z",
				AgentTimestamp: "2026-03-18T13:07:02.053Z",
				Provenance:     provenance("numbered-original"),
			}},
			nullNumber: 9,
			want: []storedRow{
				{numberNull: true, humanText: "duplicated turn", agentText: "duplicated answer"},
				{number: 6, humanText: "duplicated turn", agentText: "duplicated answer",
					model: "numbered-original"},
			},
		},
		{
			name: "a textless numbered original wins over its numberless duplicate",
			stored: []parsers.Exchange{
				{Number: 6, HumanTimestamp: "2026-03-18T13:08:01.053000+00:00",
					AgentTimestamp: "2026-03-18T13:08:02.053000+00:00"},
				{Number: 9, HumanText: "recovered turn", AgentText: "recovered answer",
					HumanTimestamp: "2026-03-18T13:08:01.053Z",
					AgentTimestamp: "2026-03-18T13:08:02.053Z"},
			},
			replay: []parsers.Exchange{{
				Number: 6, HumanText: "recovered turn", AgentText: "recovered answer",
				HumanTimestamp: "2026-03-18T13:08:01.053Z",
				AgentTimestamp: "2026-03-18T13:08:02.053Z",
				Provenance:     provenance("numbered-original"),
			}},
			nullNumber: 9,
			want: []storedRow{
				{numberNull: true, humanText: "recovered turn", agentText: "recovered answer"},
				{number: 6, agentText: "recovered answer", model: "numbered-original"},
			},
		},
		{
			name: "textless timestamp peers stay a counted conflict",
			stored: []parsers.Exchange{
				{Number: 1, HumanTimestamp: "2026-01-10T01:29:10Z"},
				{Number: 2, HumanTimestamp: "2026-01-10T02:29:10+01:00"},
			},
			replay: []parsers.Exchange{{
				Number: 1, HumanTimestamp: "2026-01-10T01:29:10.000Z",
				Provenance: provenance("must-not-land"),
			}},
			wantConflicts: 1,
			want:          []storedRow{{number: 1}, {number: 2}},
		},
		{
			name: "timestamps precede a repeated content anchor",
			stored: []parsers.Exchange{
				{Number: 1, HumanText: "same turn", AgentText: "same answer",
					HumanTimestamp: "2026-01-01T00:00:01Z", AgentTimestamp: "2026-01-01T00:00:02Z"},
				{Number: 7, HumanText: "same turn", AgentText: "same answer",
					HumanTimestamp: "2026-01-01T00:00:03Z", AgentTimestamp: "2026-01-01T00:00:04Z"},
			},
			replay: []parsers.Exchange{{
				Number: 1, HumanText: "same turn", AgentText: "same answer",
				HumanTimestamp: "2026-01-01T00:00:03Z", AgentTimestamp: "2026-01-01T00:00:04Z",
				Provenance: provenance("timestamp-match"),
			}},
			want: []storedRow{
				{number: 1, humanText: "same turn", agentText: "same answer"},
				{number: 7, humanText: "same turn", agentText: "same answer", model: "timestamp-match"},
			},
		},
		{
			name: "unique timestamp conflicts stop matching",
			stored: []parsers.Exchange{
				{Number: 1, HumanText: "timestamp turn", AgentText: "timestamp answer",
					HumanTimestamp: "2026-01-01T00:00:05Z", AgentTimestamp: "2026-01-01T00:00:06Z"},
				{Number: 2, HumanText: "content turn", AgentText: "content answer",
					HumanTimestamp: "2026-01-01T00:00:07Z", AgentTimestamp: "2026-01-01T00:00:08Z"},
			},
			replay: []parsers.Exchange{{
				Number: 3, HumanText: "content turn", AgentText: "content answer",
				HumanTimestamp: "2026-01-01T00:00:05Z", AgentTimestamp: "2026-01-01T00:00:06Z",
				Provenance: provenance("must-not-land"),
			}},
			wantConflicts: 1,
			want: []storedRow{
				{number: 1, humanText: "timestamp turn", agentText: "timestamp answer"},
				{number: 2, humanText: "content turn", agentText: "content answer"},
			},
		},
		{
			name: "historical rows are claimed once per replay",
			stored: []parsers.Exchange{{
				Number: 1, HumanText: "repeated turn", AgentText: "repeated answer",
				HumanTimestamp: "2026-01-01T00:00:09Z", AgentTimestamp: "2026-01-01T00:00:10Z",
			}},
			replay: []parsers.Exchange{
				{Number: 1, HumanText: "repeated turn", AgentText: "repeated answer",
					HumanTimestamp: "2026-01-01T00:00:09Z", AgentTimestamp: "2026-01-01T00:00:10Z",
					Provenance: provenance("historical-match")},
				{Number: 2, HumanText: "repeated turn", AgentText: "repeated answer",
					HumanTimestamp: "2026-01-01T00:00:11Z", AgentTimestamp: "2026-01-01T00:00:12Z",
					Provenance: provenance("new-exchange")},
			},
			wantInserted: 1,
			want: []storedRow{
				{number: 1, humanText: "repeated turn", agentText: "repeated answer", model: "historical-match"},
				{number: 2, humanText: "repeated turn", agentText: "repeated answer", model: "new-exchange"},
			},
		},
		{
			name: "one unclaimed timestamp candidate remains matchable",
			stored: []parsers.Exchange{
				{Number: 1, HumanText: "first timestamp turn", AgentText: "first timestamp answer",
					HumanTimestamp: "2026-01-01T00:00:13Z", AgentTimestamp: "2026-01-01T00:00:14Z"},
				{Number: 2, HumanText: "second timestamp turn", AgentText: "second timestamp answer",
					HumanTimestamp: "2026-01-01T00:00:13Z", AgentTimestamp: "2026-01-01T00:00:14Z"},
			},
			replay: []parsers.Exchange{
				{Number: 1, HumanText: "first timestamp turn", AgentText: "first timestamp answer",
					HumanTimestamp: "2026-01-01T00:00:13Z", AgentTimestamp: "2026-01-01T00:00:14Z",
					Provenance: provenance("first-match")},
				{Number: 2, HumanText: "second timestamp turn", AgentText: "second timestamp answer",
					HumanTimestamp: "2026-01-01T00:00:13Z", AgentTimestamp: "2026-01-01T00:00:14Z",
					Provenance: provenance("second-match")},
			},
			want: []storedRow{
				{number: 1, humanText: "first timestamp turn", agentText: "first timestamp answer", model: "first-match"},
				{number: 2, humanText: "second timestamp turn", agentText: "second timestamp answer", model: "second-match"},
			},
		},
		{
			name: "incompatible remaining timestamp candidate reports conflict",
			stored: []parsers.Exchange{
				{Number: 1, HumanText: "claimed timestamp turn", AgentText: "claimed timestamp answer",
					HumanTimestamp: "2026-01-01T00:00:15Z", AgentTimestamp: "2026-01-01T00:00:16Z"},
				{Number: 2, HumanText: "remaining timestamp turn", AgentText: "remaining timestamp answer",
					HumanTimestamp: "2026-01-01T00:00:15Z", AgentTimestamp: "2026-01-01T00:00:16Z"},
			},
			replay: []parsers.Exchange{
				{Number: 1, HumanText: "claimed timestamp turn", AgentText: "claimed timestamp answer",
					HumanTimestamp: "2026-01-01T00:00:15Z", AgentTimestamp: "2026-01-01T00:00:16Z",
					Provenance: provenance("claimed-match")},
				{Number: 2, HumanText: "different timestamp turn", AgentText: "different timestamp answer",
					HumanTimestamp: "2026-01-01T00:00:15Z", AgentTimestamp: "2026-01-01T00:00:16Z",
					Provenance: provenance("must-not-land")},
			},
			wantConflicts: 1,
			want: []storedRow{
				{number: 1, humanText: "claimed timestamp turn", agentText: "claimed timestamp answer", model: "claimed-match"},
				{number: 2, humanText: "remaining timestamp turn", agentText: "remaining timestamp answer"},
			},
		},
		{
			name: "component boundaries cannot collide",
			stored: []parsers.Exchange{{
				Number: 1, HumanText: "a", AgentText: "\x00b",
			}},
			replay: []parsers.Exchange{{
				Number: 1, HumanText: "a\x00", AgentText: "b", Provenance: provenance("new-turn"),
			}},
			wantInserted: 1,
			want: []storedRow{
				{number: 1, humanText: "a", agentText: "\x00b"},
				{number: 2, humanText: "a\x00", agentText: "b", model: "new-turn"},
			},
		},
		{
			name: "occupied numbers preserve new exchanges",
			stored: []parsers.Exchange{{
				Number: 1, HumanText: "historical turn", AgentText: "historical answer",
			}},
			replay: []parsers.Exchange{
				{Number: 1, HumanText: "new preceding turn", AgentText: "new preceding answer",
					Provenance: provenance("new-exchange")},
				{Number: 2, HumanText: "historical turn", AgentText: "historical answer",
					Provenance: provenance("historical-match")},
			},
			wantInserted: 1,
			want: []storedRow{
				{number: 1, humanText: "historical turn", agentText: "historical answer", model: "historical-match"},
				{number: 2, humanText: "new preceding turn", agentText: "new preceding answer", model: "new-exchange"},
			},
		},
		{
			name: "ambiguous anchors remain untouched",
			stored: []parsers.Exchange{
				{Number: 7, HumanText: "repeated turn", AgentText: "repeated answer"},
				{Number: 8, HumanText: "repeated turn", AgentText: "repeated answer"},
			},
			replay: []parsers.Exchange{{
				Number: 2, HumanText: "repeated turn", AgentText: "repeated answer",
				Provenance: provenance("must-not-land"),
			}},
			wantConflicts: 1,
			want: []storedRow{
				{number: 7, humanText: "repeated turn", agentText: "repeated answer"},
				{number: 8, humanText: "repeated turn", agentText: "repeated answer"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
			write(test.stored)
			if test.nullNumber != 0 {
				if _, err := db.SQL().Exec(`UPDATE exchanges SET exchange_number = NULL
					WHERE session_id = 'synthetic-anchor-safety' AND exchange_number = ?`,
					test.nullNumber); err != nil {
					t.Fatal(err)
				}
			}
			if counts := write(test.replay); counts.Exchanges != test.wantInserted ||
				counts.AnchorConflicts != test.wantConflicts ||
				counts.ThinkingBlocksDiscarded != test.wantThinkingDiscards {
				t.Fatalf("first replay counts = %+v, want exchanges/conflicts/thinking discards = %d/%d/%d",
					counts, test.wantInserted, test.wantConflicts, test.wantThinkingDiscards)
			}
			if counts := write(test.replay); counts.Exchanges != 0 ||
				counts.AnchorConflicts != test.wantConflicts ||
				counts.ThinkingBlocksDiscarded != test.wantThinkingDiscards {
				t.Fatalf("second replay counts = %+v, want exchanges/conflicts/thinking discards = 0/%d/%d",
					counts, test.wantConflicts, test.wantThinkingDiscards)
			}

			rows, err := db.SQL().Query(`SELECT exchange_number, COALESCE(human_text, ''),
				COALESCE(agent_text, ''), COALESCE(model, '') FROM exchanges
				WHERE session_id = 'synthetic-anchor-safety' ORDER BY exchange_number`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var got []storedRow
			for rows.Next() {
				var row storedRow
				var number sql.NullInt64
				if err := rows.Scan(&number, &row.humanText, &row.agentText, &row.model); err != nil {
					t.Fatal(err)
				}
				row.number = int(number.Int64)
				row.numberNull = !number.Valid
				got = append(got, row)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("stored rows = %+v, want %+v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("stored rows = %+v, want %+v", got, test.want)
				}
			}
		})
	}
}

func TestAnchorConflictsUseTheIngestDiscardReport(t *testing.T) {
	target := Target{Path: "synthetic-session.jsonl", Kind: parsers.KindClaudeSession,
		SourceAgent: "claude"}
	result := Result{Sources: map[string]*Counts{}}
	result.recordWritten(target, Counts{AnchorConflicts: 2, ThinkingBlocksDiscarded: 1})
	if result.RecordsDiscarded != 3 || len(result.DiscardDetails) != 3 ||
		len(result.DiscardSummary) != 2 || result.DiscardSummary[0].Count != 2 ||
		result.DiscardSummary[0].Reason != anchorConflictReason ||
		result.DiscardSummary[1].Count != 1 ||
		result.DiscardSummary[1].Reason != thinkingWithoutNumberReason ||
		result.Sources["claude"].AnchorConflicts != 2 ||
		result.Sources["claude"].ThinkingBlocksDiscarded != 1 {
		t.Fatalf("anchor conflict report = %+v", result)
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
