package service_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/hooks"
	"github.com/thellmwhisperer/la-roca/internal/service"
)

// Job J3: an agent that starts up receives what it should already know. That is
// the rostered pills plus the recent handoffs, under the measured budget, and
// the reads are the service's because a hook reaches the kernel through the CLI
// and never through the database.

func TestASessionReceivesItsPillsAndItsRecentHandoffs(t *testing.T) {
	svc := serviceWithPillsAndHandoffs(t)

	answer, err := svc.SessionContext(context.Background(), service.ContextRequest{})
	if err != nil {
		t.Fatalf("SessionContext: %v", err)
	}
	if !strings.Contains(answer.Context, "never break the build") {
		t.Errorf("the pill was not served: %q", answer.Context)
	}
	if !strings.Contains(answer.Context, "the third handoff") {
		t.Errorf("the newest handoff was not served: %q", answer.Context)
	}
	if answer.Budget.Used == 0 {
		t.Error("the budget report measured nothing")
	}
	if answer.Budget.Limit != hooks.DefaultMaxChars {
		t.Errorf("limit = %d, want the default %d",
			answer.Budget.Limit, hooks.DefaultMaxChars)
	}
}

// Three handoffs, newest first, is the laboratory's contract. A session that
// received every handoff it ever had would receive nothing else.
func TestOnlyTheThreeNewestHandoffsAreServed(t *testing.T) {
	svc := serviceWithPillsAndHandoffs(t)
	for _, content := range []string{"the fourth handoff", "the fifth handoff"} {
		storeHandoff(t, svc, content)
	}

	answer, err := svc.SessionContext(context.Background(), service.ContextRequest{})
	if err != nil {
		t.Fatalf("SessionContext: %v", err)
	}
	if strings.Contains(answer.Context, "the first handoff") {
		t.Error("a handoff older than the three newest was served")
	}
	for _, wanted := range []string{"the third", "the fourth", "the fifth"} {
		if !strings.Contains(answer.Context, wanted) {
			t.Errorf("%q was not among the three newest served", wanted)
		}
	}
}

func TestTheLatestHandoffIsServedOnItsOwn(t *testing.T) {
	svc := serviceWithPillsAndHandoffs(t)

	answer, err := svc.LatestHandoff(context.Background(), service.ContextRequest{})
	if err != nil {
		t.Fatalf("LatestHandoff: %v", err)
	}
	if !strings.Contains(answer.Context, "the third handoff") {
		t.Errorf("the newest handoff was not served: %q", answer.Context)
	}
	if strings.Contains(answer.Context, "the second handoff") {
		t.Error("more than the latest handoff was served")
	}
	if strings.Contains(answer.Context, "never break the build") {
		t.Error("the pills were served by the command that only serves the handoff")
	}
}

// The declared limit is obeyed to the character, because it is what an operator
// reaches for when the injection is crowding out their own prompt.
func TestTheDeclaredLimitIsObeyed(t *testing.T) {
	svc := serviceWithPillsAndHandoffs(t)

	answer, err := svc.SessionContext(context.Background(),
		service.ContextRequest{MaxChars: 400})
	if err != nil {
		t.Fatalf("SessionContext: %v", err)
	}
	if len(answer.Context) > 400 {
		t.Errorf("%d characters over a limit of 400", len(answer.Context))
	}
	if answer.Budget.Limit != 400 {
		t.Errorf("limit = %d, want 400", answer.Budget.Limit)
	}
}

// The roster is decided by data: what La Roca serves is its active pills, and a
// pill leaves the roster by saying so in its own metadata.
func TestAPillLeavesTheRosterBySayingSoInItsMetadata(t *testing.T) {
	svc := serviceWithPillsAndHandoffs(t)
	if _, err := svc.Store(context.Background(), service.StoreRequest{
		Layer: "pill", Content: "a pill nobody wants at startup",
		Surface: service.SurfaceCLI,
		Metadata: map[string]any{
			"pill_slug": "quiet", "pill_title": "Quiet", "session_start": false,
		},
	}); err != nil {
		t.Fatalf("store the pill: %v", err)
	}

	answer, err := svc.SessionContext(context.Background(), service.ContextRequest{})
	if err != nil {
		t.Fatalf("SessionContext: %v", err)
	}
	if strings.Contains(answer.Context, "nobody wants at startup") {
		t.Error("a pill that opted out was served anyway")
	}
}

// The roster the data decides is served in the order the pills declare, and the
// slug breaks the tie. Nothing pinned this while every fixture had one pill in
// it, so the order was whatever a map handed back: two pills with the same
// declared order came out one way on one run and the other way on the next, and
// a session's base context is not a thing that may shuffle between sessions.
func TestTheServedRosterIsOrderedByTheDeclaredOrderAndThenBySlug(t *testing.T) {
	svc := serviceWithPillsAndHandoffs(t) // this one carries "build" at order 1
	ctx := context.Background()
	for _, pill := range []struct {
		slug, title string
		order       int
	}{
		{"zulu", "Zulu", 0},     // ahead of "build" on the order alone
		{"alpha", "Alpha", 1},   // ties with "build" and wins on the slug
		{"yankee", "Yankee", 9}, // behind both
	} {
		if _, err := svc.Store(ctx, service.StoreRequest{
			Layer: "pill", Content: "the " + pill.slug + " pill",
			Surface: service.SurfaceCLI,
			Metadata: map[string]any{
				"pill_slug": pill.slug, "pill_title": pill.title,
				"pill_order": pill.order,
			},
		}); err != nil {
			t.Fatalf("store the %s pill: %v", pill.slug, err)
		}
	}

	answer, err := svc.SessionContext(ctx, service.ContextRequest{MaxChars: 4000})
	if err != nil {
		t.Fatalf("SessionContext: %v", err)
	}
	previous := -1
	for _, title := range []string{"[Zulu]", "[Alpha]", "[Build]", "[Yankee]"} {
		at := strings.Index(answer.Context, title)
		if at < 0 {
			t.Fatalf("the pill %s was not served:\n%s", title, answer.Context)
		}
		if at < previous {
			t.Errorf("%s was served out of order:\n%s", title, answer.Context)
		}
		previous = at
	}
}

// And an operator overrides the whole roster for one session, including turning
// it off by declaring it empty.
func TestTheDeclaredRosterOverridesWhatTheDataWouldServe(t *testing.T) {
	svc := serviceWithPillsAndHandoffs(t)
	ctx := context.Background()

	empty, err := svc.SessionContext(ctx, service.ContextRequest{
		Roster: []string{}, RosterDeclared: true,
	})
	if err != nil {
		t.Fatalf("SessionContext: %v", err)
	}
	if strings.Contains(empty.Context, "never break the build") {
		t.Error("a roster declared empty still served a pill")
	}
	if !strings.Contains(empty.Context, "the third handoff") {
		t.Error("turning the pills off also turned the handoffs off")
	}

	named, err := svc.SessionContext(ctx, service.ContextRequest{
		Roster: []string{"build"}, RosterDeclared: true,
	})
	if err != nil {
		t.Fatalf("SessionContext: %v", err)
	}
	if !strings.Contains(named.Context, "never break the build") {
		t.Error("a roster naming a pill did not serve it")
	}
}

// Recording a handoff is one call into the write primitive, so validation,
// layer normalization and deduplication are the same here as for any writer.
func TestRecordingAHandoffGoesThroughTheWritePrimitive(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	ctx := context.Background()

	result, err := svc.RecordHandoff(ctx, service.HandoffRequest{
		Trigger: service.TriggerSessionEnd,
		Session: "abc-123",
		CWD:     "/somewhere",
		Surface: service.SurfaceCLI,
	})
	if err != nil {
		t.Fatalf("RecordHandoff: %v", err)
	}
	if result.ID == 0 {
		t.Fatal("the handoff was not written")
	}
	if result.Layer != "handoff" {
		t.Errorf("layer = %q, want handoff", result.Layer)
	}

	var content string
	if err := svc.DB().SQL().QueryRow(
		"SELECT content FROM memories WHERE id = ?", result.ID).Scan(&content); err != nil {
		t.Fatalf("read it back: %v", err)
	}
	for _, wanted := range []string{"abc-123", "/somewhere", "SessionEnd"} {
		if !strings.Contains(content, wanted) {
			t.Errorf("the handoff does not say %q: %q", wanted, content)
		}
	}
	metadata := metadataOf(t, svc, result.ID)
	if metadata["session_id"] != "abc-123" || metadata["trigger"] != service.TriggerSessionEnd {
		t.Errorf("the handoff's metadata does not identify it: %v", metadata)
	}
}

func TestAnUnknownTriggerNamesTheOnesThatExist(t *testing.T) {
	svc, _ := serviceWithPaths(t)

	_, err := svc.RecordHandoff(context.Background(), service.HandoffRequest{
		Trigger: "whenever",
	})
	if err == nil {
		t.Fatal("an unknown trigger was accepted")
	}
	for _, trigger := range []string{service.TriggerPreCompact, service.TriggerSessionEnd} {
		if !strings.Contains(err.Error(), trigger) {
			t.Errorf("the error %q does not name the trigger %q", err, trigger)
		}
	}
}

// The reading half answers on an installation that has nothing in it: a fresh
// session on a fresh machine receives an empty block, not a failure.
func TestAFreshInstallationServesAnEmptyBlockAndNotAFailure(t *testing.T) {
	svc, _ := serviceWithPaths(t)

	answer, err := svc.SessionContext(context.Background(), service.ContextRequest{})
	if err != nil {
		t.Fatalf("SessionContext on an empty installation: %v", err)
	}
	if answer.Context != "" {
		t.Errorf("context = %q, want nothing on an installation with nothing in it",
			answer.Context)
	}
}

// --- the harness ---

func serviceWithPillsAndHandoffs(t *testing.T) *service.Service {
	t.Helper()
	svc, _ := serviceWithPaths(t)
	ctx := context.Background()
	if _, err := svc.Store(ctx, service.StoreRequest{
		Layer: "pill", Content: "# Build\nnever break the build",
		Surface: service.SurfaceCLI,
		Metadata: map[string]any{
			"pill_slug": "build", "pill_title": "Build", "pill_order": 1,
		},
	}); err != nil {
		t.Fatalf("store the pill: %v", err)
	}
	for _, content := range []string{
		"the first handoff", "the second handoff", "the third handoff",
	} {
		storeHandoff(t, svc, content)
	}
	return svc
}

// storeHandoff writes one handoff and dates it by its own identity, so that
// what is being measured is really recency and not the order SQLite happened to
// return the rows in. The identity restarts at one per test database, so the
// dates are monotonic inside each test and shared between none.
func storeHandoff(t *testing.T, svc *service.Service, content string) {
	t.Helper()
	result, err := svc.Store(context.Background(), service.StoreRequest{
		Layer: "handoff", Content: content, Surface: service.SurfaceCLI,
	})
	if err != nil {
		t.Fatalf("store the handoff: %v", err)
	}
	if _, err := svc.DB().SQL().Exec(
		"UPDATE memories SET created_at = ? WHERE id = ?",
		fmt.Sprintf("2026-08-05 10:%02d:00", result.ID), result.ID); err != nil {
		t.Fatalf("date the handoff: %v", err)
	}
}
