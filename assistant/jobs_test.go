package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"characters/llm"
	"characters/store"
)

// Day-close and summary tests use the real store and the fake model: what
// matters is exactly-once closing, retries, validation and that a job never
// blocks a check-in.

func markClosed(t *testing.T, e *Engine, today time.Time, from, to int) {
	t.Helper()
	for i := from; i <= to; i++ {
		day := today.AddDate(0, 0, -i)
		if err := e.Store.SkipCheckin(context.Background(), day, "close"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCloseDayAppliesFacts(t *testing.T) {
	now := local(9, 0)
	e, st, fake := newTestEngine(t, now, llm.FakeStep{Text: `{"facts":[
		{"op":"ADD","text":"mom is visiting in January","category":"person","core":true},
		{"op":"SUPERSEDE","id":1,"text":"lives in Jakarta","category":"circumstance","core":true}
	]}`})
	ctx := context.Background()
	yesterday := LogicalDay(now, testLoc, 4*time.Hour).AddDate(0, 0, -1)
	markClosed(t, e, LogicalDay(now, testLoc, 4*time.Hour), 2, 7)

	// A fact to supersede, and a conversation from yesterday (the daily thread
	// is selected by conversations.day, like a persona chat would be by date).
	old, err := st.AddFact(ctx, store.Fact{Text: "lives in Bali", Category: "circumstance", Core: true, ValidFrom: yesterday.AddDate(-2, 0, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateConversation(ctx, store.Conversation{ID: "daily:" + store.DayString(yesterday), CharacterID: "assistant", Day: &yesterday}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMessage(ctx, store.Message{ConversationID: "daily:" + store.DayString(yesterday), Role: "user", Content: "I moved to Jakarta, and mom is visiting in January."}); err != nil {
		t.Fatal(err)
	}

	plan, err := e.Decide(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Close) != 1 || !plan.Close[0].Equal(yesterday) {
		t.Fatalf("close plan: %+v", plan.Close)
	}
	e.ExecuteJobs(ctx, plan)

	c, err := st.Checkin(ctx, yesterday, "close")
	if err != nil || c.Status != "sent" {
		t.Fatalf("close row: %+v, %v", c, err)
	}
	facts, _ := st.Facts(ctx, store.FactFilter{Status: []string{"active"}})
	if len(facts) != 2 {
		t.Fatalf("active facts: %+v", facts)
	}
	closed, _ := st.GetFact(ctx, old.ID)
	if closed.Status != "superseded" || closed.ValidTo == nil || !store.Day(*closed.ValidTo).Equal(yesterday.AddDate(0, 0, -1)) {
		t.Fatalf("superseded fact: %+v", closed)
	}
	// Exactly once: a second poll/execution closes nothing and calls nothing.
	again, err := e.Decide(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Close) != 0 {
		t.Fatalf("day closed twice: %+v", again.Close)
	}
	if fake.Remaining() != 0 {
		t.Fatalf("extra model calls: %d", fake.Remaining())
	}
}

func TestCloseDayFailureRetriesThenSucceeds(t *testing.T) {
	now := local(9, 0)
	e, st, _ := newTestEngine(t, now,
		llm.FakeStep{Err: errors.New("gateway down")},
		llm.FakeStep{Text: `{"facts":[{"op":"ADD","text":"works night shifts","category":"work"}]}`},
	)
	ctx := context.Background()
	today := LogicalDay(now, testLoc, 4*time.Hour)
	yesterday := today.AddDate(0, 0, -1)
	markClosed(t, e, today, 2, 7)
	if _, err := st.CreateConversation(ctx, store.Conversation{ID: "daily:" + store.DayString(yesterday), CharacterID: "assistant", Day: &yesterday}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMessage(ctx, store.Message{ConversationID: "daily:" + store.DayString(yesterday), Role: "user", Content: "I work night shifts now."}); err != nil {
		t.Fatal(err)
	}

	plan, _ := e.Decide(ctx, now)
	e.ExecuteJobs(ctx, plan)
	c, _ := st.Checkin(ctx, yesterday, "close")
	if c.Status != "failed" || c.Attempts != 1 {
		t.Fatalf("after failure: %+v", c)
	}
	if facts, _ := st.Facts(ctx, store.FactFilter{Status: []string{"active"}}); len(facts) != 0 {
		t.Fatalf("facts applied after a failed call: %+v", facts)
	}

	// The next poll retries the same day.
	retry, err := e.Decide(ctx, now.Add(5*time.Minute))
	if err != nil || len(retry.Close) != 1 {
		t.Fatalf("no retry: %+v, %v", retry.Close, err)
	}
	e.ExecuteJobs(ctx, retry)
	if c, _ := st.Checkin(ctx, yesterday, "close"); c.Status != "sent" {
		t.Fatalf("after retry: %+v", c)
	}
	if facts, _ := st.Facts(ctx, store.FactFilter{Status: []string{"active"}}); len(facts) != 1 || facts[0].Text != "works night shifts" || facts[0].SourceKind != "extraction" {
		t.Fatalf("retried facts: %+v", facts)
	}
}

func TestCloseValidatesAndLogsRejects(t *testing.T) {
	now := local(9, 0)
	e, st, _ := newTestEngine(t, now, llm.FakeStep{Text: `{"facts":[
		{"op":"SUPERSEDE","id":999,"text":"lives on Mars","category":"circumstance"},
		{"op":"ADD","text":"","category":"general"},
		{"op":"ADD","text":"likes black coffee","category":"astrology"},
		{"op":"ADD","text":"likes black coffee","category":"preference"}
	]}`})
	ctx := context.Background()
	today := LogicalDay(now, testLoc, 4*time.Hour)
	yesterday := today.AddDate(0, 0, -1)
	markClosed(t, e, today, 2, 7)
	if _, err := st.CreateConversation(ctx, store.Conversation{ID: "daily:" + store.DayString(yesterday), CharacterID: "assistant", Day: &yesterday}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMessage(ctx, store.Message{ConversationID: "daily:" + store.DayString(yesterday), Role: "user", Content: "I like black coffee."}); err != nil {
		t.Fatal(err)
	}

	plan, _ := e.Decide(ctx, now)
	e.ExecuteJobs(ctx, plan)

	facts, _ := st.Facts(ctx, store.FactFilter{Status: []string{"active"}})
	if len(facts) != 1 || facts[0].Text != "likes black coffee" {
		t.Fatalf("only the valid item should be applied: %+v", facts)
	}
	// The rejects are in the close run's context.
	all, err := st.RunsByKind(ctx, "close")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || !strings.Contains(string(all[0].Context), "rejected") ||
		!strings.Contains(string(all[0].Context), "supersede of unknown") {
		t.Fatalf("rejects not recorded: %+v", all)
	}
}

func TestJobsDoNotBlockCheckin(t *testing.T) {
	now := local(9, 0)
	e, _, _ := newTestEngine(t, now, llm.FakeStep{Text: "Morning!"})
	e.jobs.mu.Lock()
	e.jobs.busy = true
	e.jobs.mu.Unlock()

	plan := Plan{Day: LogicalDay(now, testLoc, 4*time.Hour), ConversationID: "daily:2026-12-01", Checkin: CheckinMorning}
	if res := e.Execute(context.Background(), plan); res.Status != "sent" {
		t.Fatalf("check-in waited for a close job: %+v", res)
	}
}

func TestSummaryRollsUpOlderMessages(t *testing.T) {
	now := local(9, 0)
	e, st, fake := newTestEngine(t, now,
		llm.FakeStep{Text: "Earlier: we discussed the API refactor and the report."},
		llm.FakeStep{Text: "reply"},
	)
	e.Window = 10
	ctx := context.Background()
	day := LogicalDay(now, testLoc, 4*time.Hour)
	conv := "daily:" + store.DayString(day)
	if _, err := st.CreateConversation(ctx, store.Conversation{ID: conv, CharacterID: "assistant", Day: &day}); err != nil {
		t.Fatal(err)
	}
	var lastID int64
	var ids []int64
	for i := 1; i <= 40; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		m, err := st.AddMessage(ctx, store.Message{ConversationID: conv, Role: role, Content: strings.Repeat("x", 5) + " message " + store.DayString(day)})
		if err != nil {
			t.Fatal(err)
		}
		lastID = m.ID
		ids = append(ids, m.ID)
	}
	// A message forgotten before the run must not make the summary abort
	// forever; it is already excluded from the prompt by the fresh loop.
	if err := st.ForgetMessage(ctx, ids[5]); err != nil {
		t.Fatal(err)
	}

	if !e.summaryNeeded(ctx, day, e.settings(ctx)) {
		t.Fatal("summary not needed after 40 messages with a 10-message window")
	}
	e.maybeSummarize(ctx, e.settings(ctx))

	updated, err := st.GetConversation(ctx, conv)
	if err != nil || updated.Summary == "" || updated.SummaryUpto == nil {
		t.Fatalf("summary not stored: %+v, %v", updated, err)
	}
	if *updated.SummaryUpto >= lastID {
		t.Fatalf("summary covers the live window: upto %d of %d", *updated.SummaryUpto, lastID)
	}
	if !strings.Contains(updated.Summary, "API refactor") {
		t.Fatalf("summary: %q", updated.Summary)
	}

	// The next turn sees the summary and not the summarized messages.
	r := e.Runner
	if _, err := r.Turn(ctx, TurnRequest{
		ConversationID: conv, Character: e.Char, Input: "what next?",
	}, nil); err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests()
	system := reqs[len(reqs)-1].System
	if !strings.Contains(system, "<summary>") || !strings.Contains(system, "API refactor") {
		t.Fatalf("summary missing from context:\n%s", system)
	}
}

func TestValidateCloseDocRejectsDuplicateSupersede(t *testing.T) {
	facts := []store.Fact{{ID: 1, Text: "lives in Bali", Status: "active", Category: "circumstance"}}
	raw := json.RawMessage(`{"facts":[
		{"op":"SUPERSEDE","id":1,"text":"lives in Jakarta","category":"circumstance"},
		{"op":"SUPERSEDE","id":1,"text":"lives in Bandung","category":"circumstance"}
	]}`)
	ops, rejects := validateCloseDoc(raw, facts, nil)
	if len(ops) != 1 || ops[0].Text != "lives in Jakarta" {
		t.Fatalf("ops: %+v", ops)
	}
	if len(rejects) != 1 || !strings.Contains(rejects[0], "inactive") {
		t.Fatalf("rejects: %+v", rejects)
	}
}

func TestValidateCloseDocChecksSourceMessage(t *testing.T) {
	facts := []store.Fact{{ID: 1, Text: "lives in Bali", Status: "active", Category: "circumstance"}}
	msgs := []store.DayMessage{{ID: 5}}
	raw := json.RawMessage(`{"facts":[
		{"op":"ADD","text":"has a sister named Ana","category":"person","source_message_id":99},
		{"op":"ADD","text":"has a sister named Ana","category":"person","source_message_id":5}
	]}`)
	ops, rejects := validateCloseDoc(raw, facts, msgs)
	if len(ops) != 1 || ops[0].SourceMessageID == nil || *ops[0].SourceMessageID != 5 {
		t.Fatalf("ops: %+v", ops)
	}
	if len(rejects) != 1 || !strings.Contains(rejects[0], "not from this day") {
		t.Fatalf("rejects: %+v", rejects)
	}
}
