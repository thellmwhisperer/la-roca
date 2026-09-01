package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/store"
	"github.com/thellmwhisperer/la-roca/pkg/incrementality"
	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

const harvestCursorSession = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func TestGrowingSessionAppendsWithoutVersioningOldRows(t *testing.T) {
	home, workspace, sessionPath := harvestWorkspace(t)
	writeHarvestTranscript(t, sessionPath, workspace, 1)

	db := corpusDatabase(t)
	options := Options{Roots: resolveWorkspaceRoots(home, workspace)}
	ctx := context.Background()

	first, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Errors != 0 {
		t.Fatalf("first ingest errors=%+v", first.ErrorDetails)
	}
	if got := countRows(t, db.SQL(), "exchanges"); got != 1 {
		t.Fatalf("after pass 1 exchanges = %d, want 1", got)
	}
	firstID := harvestExchangeID(t, db, 1)
	if harvestVersionCount(t, db) != 0 {
		t.Fatalf("pass 1 wrote lineage for a first landing")
	}

	writeHarvestTranscript(t, sessionPath, workspace, 2)
	second, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Errors != 0 {
		t.Fatalf("second ingest errors=%+v", second.ErrorDetails)
	}
	if second.Delta.Exchanges != 1 {
		t.Fatalf("pass 2 delta exchanges = %d, want 1 appended", second.Delta.Exchanges)
	}
	if got := countRows(t, db.SQL(), "exchanges"); got != 2 {
		t.Fatalf("after pass 2 exchanges = %d, want 2", got)
	}
	if harvestExchangeID(t, db, 1) != firstID {
		t.Fatal("pass 2 rewrote the already-ingested exchange")
	}
	if harvestVersionCount(t, db) != 0 {
		t.Fatalf("pass 2 versioned an unchanged row")
	}

	writeHarvestTranscript(t, sessionPath, workspace, 3)
	third, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if third.Errors != 0 {
		t.Fatalf("third ingest errors=%+v", third.ErrorDetails)
	}
	if third.Delta.Exchanges != 1 {
		t.Fatalf("pass 3 delta exchanges = %d, want 1 appended", third.Delta.Exchanges)
	}
	if got := countRows(t, db.SQL(), "exchanges"); got != 3 {
		t.Fatalf("after pass 3 exchanges = %d, want 3", got)
	}
	if harvestExchangeID(t, db, 1) != firstID {
		t.Fatal("pass 3 rewrote the already-ingested exchange")
	}
	if harvestVersionCount(t, db) != 0 {
		t.Fatalf("pass 3 versioned an unchanged row")
	}
	assertHarvestCursorState(t, db, sessionPath, 3, true)

	rewriteHarvestExchange(t, sessionPath, workspace)
	fourth, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Errors != 0 {
		t.Fatalf("rewrite ingest errors=%+v", fourth.ErrorDetails)
	}
	if got := countRows(t, db.SQL(), "exchanges"); got != 3 {
		t.Fatalf("after rewrite exchanges = %d, want 3 current rows", got)
	}
	if harvestVersionCount(t, db) != 1 {
		t.Fatalf("rewrite lineage rows = %d, want 1", harvestVersionCount(t, db))
	}
	assertLineageHasNoContent(t, db)
}

func TestAppendableSessionParsersUseStoredExchangeCursor(t *testing.T) {
	claudeFirst := harvestTurn(1, "/w/demo", "question 1", "answer 1")
	claudeSecond := harvestTurn(2, "/w/demo", "question 2", "answer 2")
	cases := []struct {
		name, sessionID, first, second string
		kind                           parsers.Kind
		target                         Target
	}{
		{name: "claude", kind: parsers.KindClaudeSession, sessionID: harvestCursorSession,
			first: claudeFirst, second: claudeSecond},
		{name: "cowork", kind: parsers.KindCoworkAudit, sessionID: "cowork-1",
			first: claudeFirst, second: claudeSecond},
		{name: "codex", kind: parsers.KindCodexSession, sessionID: "codex-1",
			first: `{"type":"session_meta","payload":{"id":"codex-1","cwd":"/w/demo","timestamp":"2026-08-01T10:00:00Z","model_provider":"openai"}}` + "\n" +
				`{"type":"event_msg","timestamp":"2026-08-01T10:00:01Z","payload":{"type":"user_message","message":"question 1"}}` + "\n" +
				`{"type":"event_msg","timestamp":"2026-08-01T10:00:02Z","payload":{"type":"task_complete","last_agent_message":"answer 1"}}` + "\n",
			second: `{"type":"event_msg","timestamp":"2026-08-01T10:00:03Z","payload":{"type":"user_message","message":"question 2"}}` + "\n" +
				`{"type":"event_msg","timestamp":"2026-08-01T10:00:04Z","payload":{"type":"task_complete","last_agent_message":"answer 2"}}` + "\n"},
		{name: "codex history", kind: parsers.KindCodexHistory, sessionID: "history-1",
			first:  `{"session_id":"history-1","text":"question 1","ts":1785578401}` + "\n",
			second: `{"session_id":"history-1","text":"question 2","ts":1785578402}` + "\n"},
		{name: "subagent", kind: parsers.KindSubagent, sessionID: "child-1",
			target: Target{SourceAgent: "claude-code"},
			first: `{"type":"user","sessionId":"parent-1","agentId":"child-1","message":{"content":"question 1"}}` + "\n" +
				`{"type":"assistant","sessionId":"parent-1","agentId":"child-1","message":{"content":"answer 1"}}` + "\n",
			second: `{"type":"user","sessionId":"parent-1","agentId":"child-1","message":{"content":"question 2"}}` + "\n" +
				`{"type":"assistant","sessionId":"parent-1","agentId":"child-1","message":{"content":"answer 2"}}` + "\n"},
		{name: "pi", kind: parsers.KindPiSession, sessionID: "pi:pi-1",
			first: `{"type":"session","version":3,"id":"pi-1","cwd":"/w/demo"}` + "\n" +
				`{"id":"u1","parentId":null,"type":"message","message":{"role":"user","content":"question 1"}}` + "\n" +
				`{"id":"a1","parentId":"u1","type":"message","message":{"role":"assistant","content":"answer 1","stopReason":"stop"}}` + "\n",
			second: `{"id":"u2","parentId":"a1","type":"message","message":{"role":"user","content":"question 2"}}` + "\n" +
				`{"id":"a2","parentId":"u2","type":"message","message":{"role":"assistant","content":"answer 2","stopReason":"stop"}}` + "\n"},
		{name: "qwen", kind: parsers.KindQwenCode, sessionID: "qwen-1",
			target: Target{SourceAgent: "qwen-code", FileName: "session.jsonl"},
			first: `{"type":"user","sessionId":"qwen-1","version":"1","message":{"parts":[{"text":"question 1"}]}}` + "\n" +
				`{"type":"assistant","sessionId":"qwen-1","version":"1","message":{"parts":[{"text":"answer 1"}]}}` + "\n",
			second: `{"type":"user","sessionId":"qwen-1","version":"1","message":{"parts":[{"text":"question 2"}]}}` + "\n" +
				`{"type":"assistant","sessionId":"qwen-1","version":"1","message":{"parts":[{"text":"answer 2"}]}}` + "\n"},
		{name: "grok", kind: parsers.KindGrokSession, sessionID: "grok-1",
			first: `{"method":"session/update","params":{"sessionId":"grok-1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"question 1"},"_meta":{"promptIndex":1}}}}` + "\n" +
				`{"method":"session/update","params":{"sessionId":"grok-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer 1"}}}}` + "\n",
			second: `{"method":"session/update","params":{"sessionId":"grok-1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"question 2"},"_meta":{"promptIndex":2}}}}` + "\n" +
				`{"method":"session/update","params":{"sessionId":"grok-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer 2"}}}}` + "\n"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if testCase.kind == parsers.KindSubagent {
				path = filepath.Join(t.TempDir(), "subagents", "child-1.jsonl")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte(testCase.first+testCase.second), 0o600); err != nil {
				t.Fatal(err)
			}
			state := harvestCursorState{
				ByteOffset: int64(len(testCase.first)), PrefixDigest: digestBytes([]byte(testCase.first)),
				ExchangeCursor: 1, ExchangeCursors: map[string]int{testCase.sessionID: 1},
				LastExchangeComplete: true, ParserVersion: readingVersion(testCase.kind),
			}
			metadata, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			target := testCase.target
			target.Path, target.Kind, target.SessionID = path, testCase.kind, testCase.sessionID
			if target.FileName == "" {
				target.FileName = filepath.Base(path)
			}
			result := Result{}
			records, reason := read(t.Context(), Options{}, target,
				incrementality.FileState{Metadata: metadata}, &result)
			if reason != "" {
				t.Fatal(reason)
			}
			if len(records.Sessions) != 1 || len(records.Sessions[0].Exchanges) != 1 {
				t.Fatalf("incremental records = %+v", records)
			}
			exchange := records.Sessions[0].Exchanges[0]
			if exchange.Number != 2 || exchange.HumanText != "question 2" {
				t.Fatalf("incremental exchange = %+v", exchange)
			}
			if testCase.kind == parsers.KindCodexSession &&
				records.Sessions[0].Metadata["model_provider"] != "openai" {
				t.Fatalf("incremental Codex metadata = %+v", records.Sessions[0].Metadata)
			}
			if cursor := result.harvestCursors[path]; cursor.ExchangeCursor != 2 ||
				cursor.ExchangeCursors[testCase.sessionID] != 2 {
				t.Fatalf("persisted cursor = %+v", cursor)
			}
		})
	}
}

func TestCompletedCodexTurnKeepsRecoveredToolWithoutIdentity(t *testing.T) {
	prefix := `{"type":"session_meta","payload":{"id":"completed-recovery"}}
{"type":"event_msg","payload":{"type":"user_message","message":"question"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}}
{"type":"response_item","payload":{"type":"function_call","call_id":"open","name":"shell","arguments":"{\"cmd\":\"inspect\"}"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}
`
	completion := `{"type":"response_item","payload":{"type":"function_call_output","call_id":"open","output":{"metadata":{"exit_code":1}}}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"answer"}}
`
	db := corpusDatabase(t)
	for _, content := range []string{prefix, prefix + completion} {
		records, err := parsers.Parse(parsers.KindCodexSession, []byte(content),
			parsers.FileMeta{SessionID: "completed-recovery"})
		if err != nil {
			t.Fatal(err)
		}
		writeHarvestRecords(t, db, records)
	}
	var count, exchangeNumber, orphaned, hadError int
	var message string
	if err := db.SQL().QueryRow(`SELECT COUNT(*), COALESCE(MAX(exchange_number), 0),
		SUM(exchange_number IS NULL), MAX(had_error), COALESCE(MAX(error_message), '')
		FROM tool_uses WHERE session_id = ?`, "completed-recovery").
		Scan(&count, &exchangeNumber, &orphaned, &hadError, &message); err != nil {
		t.Fatal(err)
	}
	if count != 1 || exchangeNumber != 0 || orphaned != 1 || hadError != 1 ||
		message != `{"metadata":{"exit_code":1}}` {
		t.Fatalf("completed recovered tool = count:%d exchange:%d orphaned:%d error:%d message:%q",
			count, exchangeNumber, orphaned, hadError, message)
	}
}

func TestCompletedRecoveredCodexExchangeKeepsNewToolAsOrphan(t *testing.T) {
	prefix := `{"type":"session_meta","payload":{"id":"completed-new-tool"}}
{"type":"event_msg","payload":{"type":"user_message","message":"question"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}
`
	tail := `{"type":"response_item","payload":{"type":"function_call","call_id":"late","name":"shell","arguments":"{\"cmd\":\"finish\"}"}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"answer"}}
`
	db, second := writeGrowingSessionInTwoPasses(t, prefix, tail, Target{
		FileName: "rollout.jsonl", Kind: parsers.KindCodexSession,
		SessionID: "completed-new-tool", SourceAgent: "codex",
	})
	if len(second.Sessions) != 1 || second.Sessions[0].Incremental {
		t.Fatalf("completed rollout did not fall back to a full read: %+v", second.Sessions)
	}
	var count, exchangeNumber, orphaned int
	var name, params string
	if err := db.SQL().QueryRow(`SELECT COUNT(*), COALESCE(MAX(exchange_number), 0),
		SUM(exchange_number IS NULL), MAX(tool_name), MAX(tool_params_summary)
		FROM tool_uses WHERE session_id = ?`, "completed-new-tool").
		Scan(&count, &exchangeNumber, &orphaned, &name, &params); err != nil {
		t.Fatal(err)
	}
	if count != 1 || exchangeNumber != 0 || orphaned != 1 || name != "shell" ||
		params != `{"cmd":"finish"}` {
		t.Fatalf("completed new tool = count:%d exchange:%d orphaned:%d name:%q params:%q",
			count, exchangeNumber, orphaned, name, params)
	}
}

func TestOrphanProjectionDoesNotDeleteAttachedTools(t *testing.T) {
	db := corpusDatabase(t)
	exchange := parsers.Exchange{
		Number: 1, HumanText: "question", AgentText: "answer",
		Tools: []parsers.ToolUse{{Name: "shell", ParamsSummary: "inspect"}},
	}
	writeHarvestRecords(t, db, parsers.Records{Sessions: []parsers.Session{{
		ID: "existing-attached-tool", ExchangeNumbersAuthoritative: true,
		Exchanges: []parsers.Exchange{exchange},
	}}})
	exchange.Tools = nil
	writeHarvestRecords(t, db, parsers.Records{Sessions: []parsers.Session{{
		ID: "existing-attached-tool", ExchangeNumbersAuthoritative: true,
		Exchanges:     []parsers.Exchange{exchange},
		OrphanedTools: []parsers.ToolUse{{Name: "interrupt", ParamsSummary: "later"}},
	}}})
	var attached, orphaned int
	if err := db.SQL().QueryRow(`SELECT
		(SELECT COUNT(*) FROM tool_uses WHERE session_id = 'existing-attached-tool'
		 AND exchange_number = 1 AND tool_name = 'shell'),
		(SELECT COUNT(*) FROM tool_uses WHERE session_id = 'existing-attached-tool'
		 AND exchange_number IS NULL AND tool_name = 'interrupt')`).Scan(&attached, &orphaned); err != nil {
		t.Fatal(err)
	}
	if attached != 1 || orphaned != 1 {
		t.Fatalf("tool rows = attached:%d orphaned:%d, want 1/1", attached, orphaned)
	}
}

func TestConflictedCodexCompletionPreservesRecoveredOrphan(t *testing.T) {
	prefix := `{"type":"session_meta","payload":{"id":"conflicted-recovery"}}
{"type":"event_msg","payload":{"type":"user_message","message":"question"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}}
{"type":"response_item","payload":{"type":"function_call","call_id":"open","name":"shell","arguments":"{\"cmd\":\"inspect\"}"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"draft"}]}}
`
	completion := `{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"final"}}
`
	db := corpusDatabase(t)
	for _, content := range []string{prefix, prefix + completion} {
		records, err := parsers.Parse(parsers.KindCodexSession, []byte(content),
			parsers.FileMeta{SessionID: "conflicted-recovery"})
		if err != nil {
			t.Fatal(err)
		}
		writeHarvestRecords(t, db, records)
	}
	var answer string
	var total, orphaned, attached int
	if err := db.SQL().QueryRow(`SELECT agent_text,
		(SELECT COUNT(*) FROM tool_uses WHERE session_id = 'conflicted-recovery'),
		(SELECT COUNT(*) FROM tool_uses WHERE session_id = 'conflicted-recovery'
		 AND exchange_number IS NULL),
		(SELECT COUNT(*) FROM tool_uses WHERE session_id = 'conflicted-recovery'
		 AND exchange_number IS NOT NULL)
		FROM exchanges WHERE session_id = 'conflicted-recovery'`).
		Scan(&answer, &total, &orphaned, &attached); err != nil {
		t.Fatal(err)
	}
	if answer != "draft" || total != 1 || orphaned != 1 || attached != 0 {
		t.Fatalf("conflicted completion = answer:%q total:%d orphaned:%d attached:%d",
			answer, total, orphaned, attached)
	}
}

func TestConflictedCodexCompletionPreservesDistinctIdenticalCalls(t *testing.T) {
	prefix := `{"type":"session_meta","payload":{"id":"conflicted-identical-calls"}}
{"type":"event_msg","payload":{"type":"user_message","message":"question"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}}
{"type":"response_item","payload":{"type":"function_call","call_id":"transitioning","name":"shell","arguments":"{\"cmd\":\"inspect\"}"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"draft"}]}}
`
	tail := `{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"final"}}
{"type":"event_msg","payload":{"type":"user_message","message":"second"}}
{"type":"response_item","payload":{"type":"function_call","call_id":"aborted","name":"shell","arguments":"{\"cmd\":\"inspect\"}"}}
{"type":"event_msg","payload":{"type":"turn_aborted"}}
`
	db, _ := writeGrowingSessionInTwoPasses(t, prefix, tail, Target{
		FileName: "rollout.jsonl", Kind: parsers.KindCodexSession,
		SessionID: "conflicted-identical-calls", SourceAgent: "codex",
	})
	rows, err := db.SQL().Query(`SELECT exchange_number, tool_name, tool_params_summary
		FROM tool_uses WHERE session_id = ? ORDER BY id`, "conflicted-identical-calls")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var exchangeNumber sql.NullInt64
		var name, params string
		if err := rows.Scan(&exchangeNumber, &name, &params); err != nil {
			t.Fatal(err)
		}
		if exchangeNumber.Valid || name != "shell" || params != `{"cmd":"inspect"}` {
			t.Fatalf("conflicted tool = exchange:%v name:%q params:%q",
				exchangeNumber, name, params)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("preserved identical calls = %d, want 2", count)
	}
}

func TestIncrementalCodexLateVerdictFallsBackToTheFullRollout(t *testing.T) {
	prefix := `{"type":"session_meta","payload":{"id":"late-verdict"}}
{"type":"event_msg","payload":{"type":"user_message","message":"first"}}
{"type":"response_item","payload":{"type":"function_call","call_id":"old","name":"shell"}}
{"type":"event_msg","payload":{"type":"user_message","message":"replacement"}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done"}}
`
	tail := `{"type":"event_msg","payload":{"type":"user_message","message":"next"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"old","output":{"metadata":{"exit_code":1}}}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done again"}}
`
	db, second := writeGrowingSessionInTwoPasses(t, prefix, tail, Target{
		FileName: "rollout.jsonl", Kind: parsers.KindCodexSession,
		SessionID: "late-verdict", SourceAgent: "codex",
	})
	if len(second.Sessions) != 1 || second.Sessions[0].Incremental {
		t.Fatalf("late-verdict reading did not fall back to the full rollout: %+v", second.Sessions)
	}
	var count, hadError int
	var message string
	if err := db.SQL().QueryRow(`SELECT COUNT(*), had_error, error_message FROM tool_uses
		WHERE session_id = ? AND exchange_number IS NULL`, "late-verdict").
		Scan(&count, &hadError, &message); err != nil {
		t.Fatal(err)
	}
	if count != 1 || hadError != 1 || message != `{"metadata":{"exit_code":1}}` {
		t.Fatalf("persisted late verdict = count:%d error:%d message:%q", count, hadError, message)
	}
}

func TestIncrementalCodexInterruptedTailsFallBackToTheFullRollout(t *testing.T) {
	prefix := `{"type":"session_meta","payload":{"id":"interrupted-tail"}}
{"type":"event_msg","payload":{"type":"user_message","message":"complete question"}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"complete answer"}}
`
	responseTail := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"interrupted"}]}}
{"type":"response_item","payload":{"type":"function_call","call_id":"interrupted-call","name":"shell","arguments":"{}"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}}
`
	eventTail := `{"type":"event_msg","payload":{"type":"user_message","message":"interrupted"}}
` + responseTail
	abort := `{"type":"event_msg","payload":{"type":"turn_aborted"}}
`
	invalidCompletion := `{"type":"event_msg","payload":{"type":"task_complete","content":{}}}
`
	cases := []struct {
		name, tail string
	}{
		{name: "aborted", tail: eventTail + abort},
		{name: "open", tail: eventTail},
		{name: "response only", tail: responseTail},
		{name: "response only abort", tail: responseTail + abort},
		{name: "parser deferred invalid completion", tail: eventTail + invalidCompletion},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db, second := writeGrowingSessionInTwoPasses(t, prefix, testCase.tail, Target{
				FileName: "rollout.jsonl", Kind: parsers.KindCodexSession,
				SessionID: "interrupted-tail", SourceAgent: "codex",
			})
			if len(second.Sessions) != 1 || second.Sessions[0].Incremental ||
				len(second.Sessions[0].Exchanges) != 1 {
				t.Fatalf("interrupted tail records = %+v", second.Sessions)
			}
			var exchanges, orphaned int
			if err := db.SQL().QueryRow(`SELECT
				(SELECT COUNT(*) FROM exchanges WHERE session_id = 'interrupted-tail'),
				(SELECT COUNT(*) FROM tool_uses WHERE session_id = 'interrupted-tail'
				 AND exchange_number IS NULL)`).Scan(&exchanges, &orphaned); err != nil {
				t.Fatal(err)
			}
			if exchanges != 1 || orphaned != 1 {
				t.Fatalf("persisted interrupted tail = exchanges:%d orphaned:%d", exchanges, orphaned)
			}
		})
	}
}

func TestIncrementalOrphanedToolsPreserveAndExtendTheFullProjection(t *testing.T) {
	db := corpusDatabase(t)
	const sessionID = "incremental-orphan-tools"
	writeHarvestRecords(t, db, parsers.Records{Sessions: []parsers.Session{{
		ID: sessionID,
		OrphanedTools: []parsers.ToolUse{{
			Name: "aborted", ParamsSummary: "full rollout",
		}},
	}}})
	writeHarvestRecords(t, db, parsers.Records{Sessions: []parsers.Session{{
		ID: sessionID, Incremental: true, OrphanedTools: []parsers.ToolUse{},
	}}})

	var afterCompletedTail int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM tool_uses
		WHERE session_id = ? AND exchange_number IS NULL`, sessionID).Scan(&afterCompletedTail); err != nil {
		t.Fatal(err)
	}
	if afterCompletedTail != 1 {
		t.Fatalf("orphaned tools after completed tail = %d, want the existing orphan preserved",
			afterCompletedTail)
	}

	writeHarvestRecords(t, db, parsers.Records{Sessions: []parsers.Session{{
		ID: sessionID, Incremental: true,
		OrphanedTools: []parsers.ToolUse{{Name: "superseded", ParamsSummary: "tail rollout"}},
	}}})
	rows, err := db.SQL().Query(`SELECT tool_name FROM tool_uses
		WHERE session_id = ? AND exchange_number IS NULL ORDER BY id`, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "aborted" || names[1] != "superseded" {
		t.Fatalf("orphaned tools after orphan tail = %v, want preserved and appended rows", names)
	}
}

func TestCodexHistoryCursorRemainsPerSession(t *testing.T) {
	prefix := `{"session_id":"history-1","text":"one","ts":1785578401}` + "\n" +
		`{"session_id":"history-2","text":"other","ts":1785578402}` + "\n"
	tail := `{"session_id":"history-1","text":"two","ts":1785578403}` + "\n"
	records, result, path := readCodexHistoryIncrement(t, prefix, tail, 1,
		map[string]int{"history-1": 1, "history-2": 1})
	if len(records.Sessions) != 1 || records.Sessions[0].ID != "history-1" ||
		len(records.Sessions[0].Exchanges) != 1 || records.Sessions[0].Exchanges[0].Number != 2 {
		t.Fatalf("incremental history records = %+v", records)
	}
	cursor := result.harvestCursors[path]
	if cursor.ExchangeCursors["history-1"] != 2 || cursor.ExchangeCursors["history-2"] != 1 {
		t.Fatalf("per-session history cursor = %+v", cursor.ExchangeCursors)
	}
}

func TestCodexHistoryCursorStartsNewSessionAtOne(t *testing.T) {
	prefix := `{"session_id":"history-1","text":"five","ts":1785578401}` + "\n"
	tail := `{"session_id":"history-2","text":"first","ts":1785578402}` + "\n"
	records, result, path := readCodexHistoryIncrement(t, prefix, tail, 5,
		map[string]int{"history-1": 5})
	if len(records.Sessions) != 1 || records.Sessions[0].ID != "history-2" ||
		len(records.Sessions[0].Exchanges) != 1 || records.Sessions[0].Exchanges[0].Number != 1 {
		t.Fatalf("new history session records = %+v", records)
	}
	cursor := result.harvestCursors[path]
	if cursor.ExchangeCursors["history-1"] != 5 || cursor.ExchangeCursors["history-2"] != 1 {
		t.Fatalf("per-session history cursor = %+v", cursor.ExchangeCursors)
	}
}

// readCodexHistoryIncrement writes a history prefix plus tail, primes the
// stored exchange cursor from the prefix, and runs one incremental read.
func readCodexHistoryIncrement(t *testing.T, prefix, tail string, exchangeCursor int,
	cursors map[string]int,
) (parsers.Records, Result, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := os.WriteFile(path, []byte(prefix+tail), 0o600); err != nil {
		t.Fatal(err)
	}
	state := harvestCursorState{
		ByteOffset: int64(len(prefix)), PrefixDigest: digestBytes([]byte(prefix)),
		ExchangeCursor: exchangeCursor, ExchangeCursors: cursors,
		LastExchangeComplete: true, ParserVersion: readingVersion(parsers.KindCodexHistory),
	}
	metadata, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	result := Result{}
	records, reason := read(t.Context(), Options{}, Target{
		Path: path, FileName: filepath.Base(path), Kind: parsers.KindCodexHistory,
	}, incrementality.FileState{Metadata: metadata}, &result)
	if reason != "" {
		t.Fatal(reason)
	}
	return records, result, path
}

func TestIncompleteGrowingSessionFallsBackToACompleteReparse(t *testing.T) {
	home, workspace, sessionPath := harvestWorkspace(t)
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	user := fmt.Sprintf("{\"type\":\"user\",\"timestamp\":\"2026-08-01T10:00:00Z\",\"cwd\":%q,\"message\":{\"content\":\"pending\"}}\n", workspace)
	if err := os.WriteFile(sessionPath, []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	db := corpusDatabase(t)
	if _, err := Run(t.Context(), db, registry(t),
		Options{Roots: resolveWorkspaceRoots(home, workspace)}); err != nil {
		t.Fatal(err)
	}
	assertHarvestCursorState(t, db, sessionPath, 0, false)
	assistant := `{"type":"assistant","timestamp":"2026-08-01T10:00:10Z","message":{"content":[{"type":"text","text":"complete"}]}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(user+assistant), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(t.Context(), db, registry(t), Options{Roots: resolveWorkspaceRoots(home, workspace)}); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db.SQL(), "exchanges"); got != 1 {
		t.Fatalf("completed fallback exchanges = %d, want 1", got)
	}
	assertHarvestCursorState(t, db, sessionPath, 1, true)
}

func TestIncompleteSubagentTailForcesACompleteReparse(t *testing.T) {
	prefix := `{"type":"user","sessionId":"parent-1","agentId":"child-1","message":{"content":"pending question"}}` + "\n"
	tail := `{"type":"user","sessionId":"parent-1","agentId":"child-1","message":{"content":"later question"}}` + "\n" +
		`{"type":"assistant","sessionId":"parent-1","agentId":"child-1","message":{"content":"later answer"}}` + "\n"
	path := filepath.Join(t.TempDir(), "subagents", "child-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(prefix), 0o600); err != nil {
		t.Fatal(err)
	}
	first := Result{}
	_, reason := read(t.Context(), Options{}, Target{
		Path: path, FileName: filepath.Base(path), Kind: parsers.KindSubagent,
		SourceAgent: "claude-code",
	}, incrementality.FileState{}, &first)
	if reason != "" {
		t.Fatal(reason)
	}
	cursor := first.harvestCursors[path]
	if cursor.LastExchangeComplete {
		t.Fatal("unpaired subagent tail was recorded as complete")
	}
	metadata, err := json.Marshal(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(prefix+tail), 0o600); err != nil {
		t.Fatal(err)
	}
	second := Result{}
	records, reason := read(t.Context(), Options{}, Target{
		Path: path, FileName: filepath.Base(path), Kind: parsers.KindSubagent,
		SourceAgent: "claude-code",
	}, incrementality.FileState{Metadata: metadata}, &second)
	if reason != "" {
		t.Fatal(reason)
	}
	if len(records.Sessions) != 1 || len(records.Sessions[0].Exchanges) != 1 ||
		records.Sessions[0].Exchanges[0].HumanText != "later question" {
		t.Fatalf("subagent fallback records = %+v", records)
	}
}

func TestIncrementalPiSessionAdvancesItsTimeline(t *testing.T) {
	prefix := `{"type":"session","version":3,"id":"pi-timeline","cwd":"/w/demo","timestamp":"2026-08-01T13:00:00Z"}` + "\n" +
		`{"id":"u1","parentId":null,"type":"message","timestamp":"2026-08-01T13:00:01Z","message":{"role":"user","content":"one"}}` + "\n" +
		`{"id":"a1","parentId":"u1","type":"message","timestamp":"2026-08-01T13:00:02Z","message":{"role":"assistant","content":"done","stopReason":"stop"}}` + "\n"
	tail := `{"id":"u2","parentId":"a1","type":"message","timestamp":"2026-08-01T13:10:00Z","message":{"role":"user","content":"two"}}` + "\n" +
		`{"id":"a2","parentId":"u2","type":"message","timestamp":"2026-08-01T13:10:02Z","message":{"role":"assistant","content":"done again","stopReason":"stop"}}` + "\n"
	db, second := writeGrowingSessionInTwoPasses(t, prefix, tail, Target{
		FileName: "session.jsonl", Kind: parsers.KindPiSession,
	})
	if len(second.Sessions) != 1 || !second.Sessions[0].Incremental {
		t.Fatalf("incremental Pi session = %+v", second.Sessions)
	}
	var started, ended string
	var duration int
	if err := db.SQL().QueryRow(`SELECT started_at, ended_at, duration_minutes FROM sessions
		WHERE session_id = 'pi:pi-timeline'`).Scan(&started, &ended, &duration); err != nil {
		t.Fatal(err)
	}
	if started != "2026-08-01T13:00:01Z" || ended != "2026-08-01T13:10:02Z" || duration != 10 {
		t.Fatalf("incremental timeline = %s to %s (%d minutes)", started, ended, duration)
	}
}

func TestSubagentAssistantExtensionRewritesOneExchangeWithLineage(t *testing.T) {
	first := `{"type":"user","sessionId":"parent-1","agentId":"child-rewrite","message":{"content":"question"}}` + "\n" +
		`{"type":"assistant","sessionId":"parent-1","agentId":"child-rewrite","message":{"content":"partial"}}` + "\n"
	second := first + `{"type":"assistant","sessionId":"parent-1","agentId":"child-rewrite","message":{"content":"complete"}}` + "\n"
	db := corpusDatabase(t)
	for _, content := range []string{first, second} {
		records, err := parsers.Parse(parsers.KindSubagent, []byte(content), parsers.FileMeta{
			Path: "/w/.claude/projects/-w-demo/session/subagents/child-rewrite.jsonl",
		})
		if err != nil {
			t.Fatal(err)
		}
		writeHarvestRecords(t, db, records)
	}
	var answer string
	if err := db.SQL().QueryRow(`SELECT agent_text FROM exchanges
		WHERE session_id = 'child-rewrite' AND exchange_number = 1`).Scan(&answer); err != nil {
		t.Fatal(err)
	}
	if answer != "partial\ncomplete" {
		t.Fatalf("rewritten subagent answer = %q", answer)
	}
	if got := harvestVersionCount(t, db); got != 1 {
		t.Fatalf("subagent rewrite lineage rows = %d, want 1", got)
	}
	assertLineageHasNoContent(t, db)
}

func TestCoworkAssistantExtensionRewritesOneExchangeWithLineage(t *testing.T) {
	first := `{"type":"user","session_id":"cowork-rewrite","_audit_timestamp":"2026-08-01T12:00:00Z","message":{"content":[{"type":"text","text":"question"}]}}` + "\n" +
		`{"type":"assistant","_audit_timestamp":"2026-08-01T12:00:04Z","message":{"content":"partial"}}` + "\n"
	second := first + `{"type":"assistant","_audit_timestamp":"2026-08-01T12:00:05Z","message":{"content":"complete"}}` + "\n"
	db := corpusDatabase(t)
	for _, content := range []string{first, second} {
		records, err := parsers.Parse(parsers.KindCoworkAudit, []byte(content), parsers.FileMeta{
			SourceAgent: "cowork", SessionID: "cowork-rewrite",
		})
		if err != nil {
			t.Fatal(err)
		}
		writeHarvestRecords(t, db, records)
	}
	var answer string
	if err := db.SQL().QueryRow(`SELECT agent_text FROM exchanges
		WHERE session_id = 'cowork-rewrite' AND exchange_number = 1`).Scan(&answer); err != nil {
		t.Fatal(err)
	}
	if answer != "partial\ncomplete" {
		t.Fatalf("rewritten cowork answer = %q", answer)
	}
	if got := harvestVersionCount(t, db); got != 1 {
		t.Fatalf("cowork rewrite lineage rows = %d, want 1", got)
	}
	assertLineageHasNoContent(t, db)
}

func TestQwenAssistantExtensionRewritesOneExchangeWithLineage(t *testing.T) {
	first := `{"type":"user","sessionId":"qwen-rewrite","timestamp":"2026-08-01T09:00:00Z","cwd":"/synthetic/harbour","version":"9.9.9","message":{"role":"user","parts":[{"text":"question"}]}}` + "\n" +
		`{"type":"assistant","sessionId":"qwen-rewrite","timestamp":"2026-08-01T09:00:01Z","cwd":"/synthetic/harbour","version":"9.9.9","message":{"role":"model","parts":[{"text":"partial"}]}}` + "\n"
	second := first + `{"type":"assistant","sessionId":"qwen-rewrite","timestamp":"2026-08-01T09:00:02Z","cwd":"/synthetic/harbour","version":"9.9.9","message":{"role":"model","parts":[{"text":"complete"}]}}` + "\n"
	db := corpusDatabase(t)
	for _, content := range []string{first, second} {
		records, err := parsers.Parse(parsers.KindQwenCode, []byte(content), parsers.FileMeta{
			SourceAgent: "qwen-code",
		})
		if err != nil {
			t.Fatal(err)
		}
		writeHarvestRecords(t, db, records)
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM exchanges WHERE session_id = 'qwen-rewrite'`).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("qwen rewrite allocated %d exchanges, want 1", count)
	}
	var answer string
	if err := db.SQL().QueryRow(`SELECT agent_text FROM exchanges
		WHERE session_id = 'qwen-rewrite' AND exchange_number = 1`).Scan(&answer); err != nil {
		t.Fatal(err)
	}
	if answer != "partial\ncomplete" {
		t.Fatalf("rewritten qwen answer = %q", answer)
	}
	if got := harvestVersionCount(t, db); got != 1 {
		t.Fatalf("qwen rewrite lineage rows = %d, want 1", got)
	}
	assertLineageHasNoContent(t, db)
}

func TestGrokAssistantExtensionRewritesOneExchangeWithLineage(t *testing.T) {
	first := `{"method":"session/update","params":{"sessionId":"grok-rewrite","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"question"},"_meta":{"promptIndex":1}}},"timestamp":1785585600}` + "\n" +
		`{"method":"session/update","params":{"sessionId":"grok-rewrite","_meta":{"promptId":"prompt-1"},"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"partial"}}},"timestamp":1785585601}` + "\n"
	second := first + `{"method":"session/update","params":{"sessionId":"grok-rewrite","_meta":{"promptId":"prompt-1"},"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"complete"}}},"timestamp":1785585602}` + "\n"
	db := corpusDatabase(t)
	for _, content := range []string{first, second} {
		records, err := parsers.Parse(parsers.KindGrokSession, []byte(content), parsers.FileMeta{
			SourceAgent: "grok", SessionID: "grok-rewrite",
		})
		if err != nil {
			t.Fatal(err)
		}
		writeHarvestRecords(t, db, records)
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM exchanges WHERE session_id = 'grok-rewrite'`).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("grok rewrite allocated %d exchanges, want 1", count)
	}
	var answer string
	if err := db.SQL().QueryRow(`SELECT agent_text FROM exchanges
		WHERE session_id = 'grok-rewrite' AND exchange_number = 1`).Scan(&answer); err != nil {
		t.Fatal(err)
	}
	if answer != "partialcomplete" {
		t.Fatalf("rewritten grok answer = %q", answer)
	}
	if got := harvestVersionCount(t, db); got != 1 {
		t.Fatalf("grok rewrite lineage rows = %d, want 1", got)
	}
	assertLineageHasNoContent(t, db)
}

func TestLineageDigestFramesEmbeddedNULBoundaries(t *testing.T) {
	db := corpusDatabase(t)
	write := func(human, agent string) {
		t.Helper()
		err := db.Write(t.Context(), func(tx *sql.Tx) error {
			_, err := writeRecords(t.Context(), tx, registry(t), nil, parsers.Records{
				Sessions: []parsers.Session{{
					ID: harvestCursorSession, SourceAgent: "claude",
					ExchangeNumbersAuthoritative: true,
					Exchanges:                    []parsers.Exchange{{Number: 1, HumanText: human, AgentText: agent}},
				}},
			})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	write("x\x00y", "z")
	write("x", "y\x00z")
	write("x\x00y", "z")
	if got := harvestVersionCount(t, db); got != 2 {
		t.Fatalf("boundary-distinct lineage rows = %d, want 2", got)
	}
}

func assertHarvestCursorState(t *testing.T, db *store.DB, path string, cursor int,
	complete bool) {
	t.Helper()
	var encoded []byte
	if err := db.SQL().QueryRow(`SELECT metadata FROM ingest_file_state WHERE path = ?`, path).
		Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var state harvestCursorState
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.ByteOffset != info.Size() || state.PrefixDigest == "" ||
		state.ExchangeCursor != cursor || state.LastExchangeComplete != complete {
		t.Fatalf("cursor state = %+v, size=%d; want cursor=%d complete=%t",
			state, info.Size(), cursor, complete)
	}
}

func writeGrowingSessionInTwoPasses(t *testing.T, prefix, tail string,
	target Target,
) (*store.DB, parsers.Records) {
	t.Helper()
	path := filepath.Join(t.TempDir(), target.FileName)
	target.Path = path
	if err := os.WriteFile(path, []byte(prefix), 0o600); err != nil {
		t.Fatal(err)
	}
	firstResult := Result{}
	first, reason := read(t.Context(), Options{}, target, incrementality.FileState{}, &firstResult)
	if reason != "" {
		t.Fatal(reason)
	}
	db := corpusDatabase(t)
	writeHarvestRecords(t, db, first)
	metadata, err := json.Marshal(firstResult.harvestCursors[path])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(prefix+tail), 0o600); err != nil {
		t.Fatal(err)
	}
	secondResult := Result{}
	second, reason := read(t.Context(), Options{}, target,
		incrementality.FileState{Metadata: metadata}, &secondResult)
	if reason != "" {
		t.Fatal(reason)
	}
	writeHarvestRecords(t, db, second)
	return db, second
}

func corpusDatabase(t *testing.T) *store.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "roca-corpus.db")
	if err := rocacorpus.ApplySchema(path); err != nil {
		t.Fatalf("corpus schema: %v", err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func writeHarvestRecords(t *testing.T, db *store.DB, records parsers.Records) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := writeRecords(t.Context(), tx, registry(t), nil, records)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func writeHarvestTranscript(t *testing.T, path, cwd string, exchanges int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := ""
	for turn := 1; turn <= exchanges; turn++ {
		body += harvestTurn(turn, cwd, fmt.Sprintf("question %d", turn),
			fmt.Sprintf("answer %d", turn))
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func rewriteHarvestExchange(t *testing.T, path, cwd string) {
	t.Helper()
	body := harvestTurn(1, cwd, "rewritten question", "rewritten answer")
	body += harvestTurn(2, cwd, "question 2", "answer 2")
	body += harvestTurn(3, cwd, "question 3", "answer 3")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func harvestTurn(turn int, cwd, question, answer string) string {
	stamp := fmt.Sprintf("2026-08-01T10:00:%02dZ", turn*10)
	return fmt.Sprintf("{\"type\":\"user\",\"timestamp\":%q,\"cwd\":%q,\"message\":{\"content\":%q}}\n"+
		"{\"type\":\"assistant\",\"timestamp\":%q,\"message\":{\"content\":[{\"type\":\"text\",\"text\":%q}]}}\n",
		stamp, cwd, question, stamp, answer)
}

func harvestExchangeID(t *testing.T, db *store.DB, number int) int64 {
	t.Helper()
	var id int64
	if err := db.SQL().QueryRow(`SELECT id FROM exchanges WHERE session_id = ? AND exchange_number = ?`,
		harvestCursorSession, number).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func harvestVersionCount(t *testing.T, db *store.DB) int {
	t.Helper()
	var count int
	err := db.SQL().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'exchange_versions'`).
		Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		return 0
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM exchange_versions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertLineageHasNoContent(t *testing.T, db *store.DB) {
	t.Helper()
	for _, column := range []string{"human_text", "agent_text", "full_text"} {
		var count int
		if err := db.SQL().QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('exchange_versions') WHERE name = ?`, column).
			Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("lineage still stores content column %s", column)
		}
	}
}

// resolveWorkspaceRoots derives roots from a home and workspace the same way
// every harvest test does, keeping the fixture spelling in one place.
func resolveWorkspaceRoots(home, workspace string) Roots {
	return ResolveRoots(Environment{GOOS: "darwin", Home: home},
		Settings{WorkspaceRoots: []string{filepath.Dir(workspace)}})
}

// harvestWorkspace creates the demo workspace and the claude session path the
// growing-session tests share.
func harvestWorkspace(t *testing.T) (home, workspace, sessionPath string) {
	t.Helper()
	home = t.TempDir()
	workspace = filepath.Join(home, "w", "demo")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionPath = filepath.Join(home, ".claude", "projects", encodeRoot(workspace),
		harvestCursorSession+".jsonl")
	return home, workspace, sessionPath
}
