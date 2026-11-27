package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"squad/llm"
	"squad/store"
	"squad/store/storetest"
)

// Day-close and summary tests use the real store and the fake model: what
// matters is exactly-once closing, retries, validation and that a job never
// blocks a check-in.

func markClosed(t *testing.T, e *Engine, today time.Time, from, to int) {
	t.Helper()
	for i := from; i <= to; i++ {
		day := today.AddDate(0, 0, -i)
		if err := e.Store.SkipCheckin(context.Background(), day, "close", e.Char.ID); err != nil {
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
	old, err := st.AddFact(ctx, store.Fact{CharacterID: "assistant", Text: "lives in Bali", Category: "circumstance", Core: true, ValidFrom: yesterday.AddDate(-2, 0, 0)})
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
	if len(plan.Close) != 1 || !plan.Close[0].Day.Equal(yesterday) || plan.Close[0].CharacterID != "assistant" {
		t.Fatalf("close plan: %+v", plan.Close)
	}
	e.ExecuteJobs(ctx, plan)

	c, err := st.Checkin(ctx, yesterday, "close", "assistant")
	if err != nil || c.Status != "sent" {
		t.Fatalf("close row: %+v, %v", c, err)
	}
	facts, _ := st.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}})
	if len(facts) != 2 {
		t.Fatalf("active facts: %+v", facts)
	}
	closed, _ := st.GetFact(ctx, "assistant", old.ID)
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
	c, _ := st.Checkin(ctx, yesterday, "close", "assistant")
	if c.Status != "failed" || c.Attempts != 1 {
		t.Fatalf("after failure: %+v", c)
	}
	if facts, _ := st.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}}); len(facts) != 0 {
		t.Fatalf("facts applied after a failed call: %+v", facts)
	}

	// The next poll retries the same day.
	retry, err := e.Decide(ctx, now.Add(5*time.Minute))
	if err != nil || len(retry.Close) != 1 {
		t.Fatalf("no retry: %+v, %v", retry.Close, err)
	}
	e.ExecuteJobs(ctx, retry)
	if c, _ := st.Checkin(ctx, yesterday, "close", "assistant"); c.Status != "sent" {
		t.Fatalf("after retry: %+v", c)
	}
	if facts, _ := st.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}}); len(facts) != 1 || facts[0].Text != "works night shifts" || facts[0].SourceKind != "extraction" {
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

	facts, _ := st.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}})
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
	if err := st.ForgetMessage(ctx, "assistant", ids[5]); err != nil {
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
	facts := []store.Fact{{ID: 1, CharacterID: "assistant", Text: "lives in Bali", Status: "active", Category: "circumstance"}}
	raw := json.RawMessage(`{"facts":[
		{"op":"SUPERSEDE","id":1,"text":"lives in Jakarta","category":"circumstance"},
		{"op":"SUPERSEDE","id":1,"text":"lives in Bandung","category":"circumstance"}
	]}`)
	ops, rejects := validateCloseDoc(raw, "assistant", facts, nil)
	if len(ops) != 1 || ops[0].Text != "lives in Jakarta" {
		t.Fatalf("ops: %+v", ops)
	}
	if len(rejects) != 1 || !strings.Contains(rejects[0], "inactive") {
		t.Fatalf("rejects: %+v", rejects)
	}
}

func TestValidateCloseDocChecksSourceMessage(t *testing.T) {
	facts := []store.Fact{{ID: 1, CharacterID: "assistant", Text: "lives in Bali", Status: "active", Category: "circumstance"}}
	msgs := []store.DayMessage{{ID: 5, CharacterID: "assistant"}}
	raw := json.RawMessage(`{"facts":[
		{"op":"ADD","text":"has a sister named Ana","category":"person","source_message_id":99},
		{"op":"ADD","text":"has a sister named Ana","category":"person","source_message_id":5}
	]}`)
	ops, rejects := validateCloseDoc(raw, "assistant", facts, msgs)
	if len(ops) != 1 || ops[0].SourceMessageID == nil || *ops[0].SourceMessageID != 5 {
		t.Fatalf("ops: %+v", ops)
	}
	if len(rejects) != 1 || !strings.Contains(rejects[0], "not from this day") {
		t.Fatalf("rejects: %+v", rejects)
	}
}

// seedDayThread adds one user message on the logical day for a character. The
// assistant gets a daily thread (selected by conversations.day); a persona gets
// a normal conversation (selected by created_at inside the day).
func seedDayThread(t *testing.T, st *store.Store, characterID string, day time.Time, content string) int64 {
	t.Helper()
	ctx := context.Background()
	var (
		convID  string
		convDay *time.Time
		at      time.Time
	)
	if characterID == "assistant" {
		convID = "daily:" + store.DayString(day)
		convDay = &day
		at = At(day, 5*time.Hour, testLoc)
	} else {
		convID = characterID + "-chat"
		at = At(day, 5*time.Hour, testLoc)
	}
	if _, err := st.CreateConversation(ctx, store.Conversation{ID: convID, CharacterID: characterID, Day: convDay}); err != nil {
		t.Fatal(err)
	}
	m, err := st.AddMessageAt(ctx, store.Message{ConversationID: convID, Role: "user", Content: content}, at)
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func TestCloseDaySplitsOwners(t *testing.T) {
	now := local(9, 0)
	e, st, _ := newTestEngine(t, now,
		// Ordering is character_id: assistant first, then ria.
		llm.FakeStep{Text: `{"facts":[{"op":"ADD","text":"assistant only fact","category":"work"}]}`},
		llm.FakeStep{Text: `{"facts":[
			{"op":"ADD","text":"ria only fact","category":"health"},
			{"op":"SUPERSEDE","id":@@ASSISTANT_FACT@@,"text":"forged supersede","category":"circumstance"},
			{"op":"ADD","text":"forged source","category":"person","source_message_id":@@ASSISTANT_MESSAGE@@}
		]}`},
	)
	ctx := context.Background()
	today := LogicalDay(now, testLoc, 4*time.Hour)
	yesterday := today.AddDate(0, 0, -1)
	markClosed(t, e, today, 2, 7)

	old, err := st.AddFact(ctx, store.Fact{CharacterID: "assistant", Text: "the real assistant fact", Category: "circumstance", Core: true, ValidFrom: yesterday.AddDate(-1, 0, 0)})
	if err != nil {
		t.Fatal(err)
	}
	assistantMsg := seedDayThread(t, st, "assistant", yesterday, "I work nights now.")
	seedDayThread(t, st, "ria", yesterday, "I think I'm getting sick.")

	plan, err := e.Decide(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Close) != 2 {
		t.Fatalf("close plan: %+v", plan.Close)
	}
	for i, want := range []string{"assistant", "ria"} {
		if plan.Close[i].CharacterID != want || !plan.Close[i].Day.Equal(yesterday) {
			t.Fatalf("close job %d: %+v", i, plan.Close[i])
		}
	}

	// The fake model is scripted before these ids exist; swap the placeholders
	// for the real ones now.
	e.Runner.models[string(llm.OpenCodeProvider)] = llm.NewFake(
		llm.FakeStep{Text: `{"facts":[{"op":"ADD","text":"assistant only fact","category":"work"}]}`},
		llm.FakeStep{Text: `{"facts":[
			{"op":"ADD","text":"ria only fact","category":"health"},
			{"op":"SUPERSEDE","id":` + strconv.FormatInt(old.ID, 10) + `,"text":"forged supersede","category":"circumstance"},
			{"op":"ADD","text":"forged source","category":"person","source_message_id":` + strconv.FormatInt(assistantMsg, 10) + `}
		]}`},
	)
	e.ExecuteJobs(ctx, plan)

	// Each fact landed on its owner, and only there.
	assistantFacts, _ := st.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}})
	if len(assistantFacts) != 2 {
		t.Fatalf("assistant facts: %+v", assistantFacts)
	}
	for _, f := range assistantFacts {
		if f.CharacterID != "assistant" || f.Text == "forged supersede" || f.Text == "forged source" {
			t.Fatalf("assistant got a foreign or forged fact: %+v", f)
		}
	}
	riaFacts, _ := st.Facts(ctx, "ria", store.FactFilter{Status: []string{"active"}})
	if len(riaFacts) != 1 || riaFacts[0].CharacterID != "ria" || riaFacts[0].Text != "ria only fact" || riaFacts[0].SourceKind != "extraction" {
		t.Fatalf("ria facts: %+v", riaFacts)
	}
	if got, _ := st.GetFact(ctx, "assistant", old.ID); got.Status != "active" {
		t.Fatalf("forged cross-owner supersede took effect: %+v", got)
	}

	// Both forged items are rejected and recorded in ria's close run.
	runs, err := st.RunsByKind(ctx, "close")
	if err != nil || len(runs) != 2 {
		t.Fatalf("close runs: %+v, %v", runs, err)
	}
	var riaRun string
	for _, r := range runs {
		if strings.Contains(string(r.Context), `"character_id": "ria"`) {
			riaRun = string(r.Context)
		}
	}
	if !strings.Contains(riaRun, "rejected") || !strings.Contains(riaRun, "supersede of unknown") ||
		!strings.Contains(riaRun, "not from this day") {
		for _, r := range runs {
			t.Logf("run %d context: %s", r.ID, r.Context)
		}
		t.Fatalf("forged items not recorded as rejects: %q", riaRun)
	}
	for _, characterID := range []string{"assistant", "ria"} {
		if c, _ := st.Checkin(ctx, yesterday, "close", characterID); c.Status != "sent" {
			t.Fatalf("%s close row: %+v", characterID, c)
		}
	}
}

func TestCloseDayRetriesOneOwnerAlone(t *testing.T) {
	now := local(9, 0)
	e, st, fake := newTestEngine(t, now,
		llm.FakeStep{Text: `{"facts":[{"op":"ADD","text":"assistant closes fine","category":"work"}]}`},
		llm.FakeStep{Err: errors.New("gateway down")},
		llm.FakeStep{Text: `{"facts":[{"op":"ADD","text":"ria closes late","category":"health"}]}`},
	)
	ctx := context.Background()
	today := LogicalDay(now, testLoc, 4*time.Hour)
	yesterday := today.AddDate(0, 0, -1)
	markClosed(t, e, today, 2, 7)
	seedDayThread(t, st, "assistant", yesterday, "I finished the report.")
	seedDayThread(t, st, "ria", yesterday, "My back hurts.")

	plan, _ := e.Decide(ctx, now)
	if len(plan.Close) != 2 {
		t.Fatalf("close plan: %+v", plan.Close)
	}
	e.ExecuteJobs(ctx, plan)

	if c, _ := st.Checkin(ctx, yesterday, "close", "assistant"); c.Status != "sent" {
		t.Fatalf("assistant close after ria failed: %+v", c)
	}
	if c, _ := st.Checkin(ctx, yesterday, "close", "ria"); c.Status != "failed" || c.Attempts != 1 {
		t.Fatalf("ria close: %+v", c)
	}
	assistantFacts, _ := st.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}})
	if len(assistantFacts) != 1 || assistantFacts[0].Text != "assistant closes fine" {
		t.Fatalf("assistant facts after ria failed: %+v", assistantFacts)
	}
	if riaFacts, _ := st.Facts(ctx, "ria", store.FactFilter{Status: []string{"active"}}); len(riaFacts) != 0 {
		t.Fatalf("ria facts after her failed call: %+v", riaFacts)
	}
	if fake.Remaining() != 1 {
		t.Fatalf("expected exactly one scripted call left, have %d", fake.Remaining())
	}

	// The next poll retries only ria, and the assistant's facts are not
	// applied a second time.
	retry, err := e.Decide(ctx, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.Close) != 1 || retry.Close[0].CharacterID != "ria" {
		t.Fatalf("retry plan: %+v", retry.Close)
	}
	e.ExecuteJobs(ctx, retry)
	if c, _ := st.Checkin(ctx, yesterday, "close", "ria"); c.Status != "sent" {
		t.Fatalf("ria close after retry: %+v", c)
	}
	assistantFacts, _ = st.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}})
	if len(assistantFacts) != 1 {
		t.Fatalf("assistant facts re-applied: %+v", assistantFacts)
	}
	if riaFacts, _ := st.Facts(ctx, "ria", store.FactFilter{Status: []string{"active"}}); len(riaFacts) != 1 || riaFacts[0].Text != "ria closes late" {
		t.Fatalf("ria facts after retry: %+v", riaFacts)
	}
	if fake.Remaining() != 0 {
		t.Fatalf("extra model calls: %d", fake.Remaining())
	}
}

// A character whose context is "none" has no memory stack, so the day-close job
// must not extract facts for it either.
func TestCloseSkipsCharactersWithMemoryOff(t *testing.T) {
	now := local(9, 0)
	e, st, fake := newTestEngine(t, now,
		llm.FakeStep{Text: `{"facts":[{"op":"ADD","text":"assistant closes","category":"work"}]}`},
	)
	e.MemoryEnabled = func(characterID string) bool { return characterID == "assistant" }
	ctx := context.Background()
	today := LogicalDay(now, testLoc, 4*time.Hour)
	yesterday := today.AddDate(0, 0, -1)
	markClosed(t, e, today, 2, 7)
	seedDayThread(t, st, "assistant", yesterday, "I finished the report.")
	seedDayThread(t, st, "mute", yesterday, "This must not become a fact.")

	plan, err := e.Decide(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Close) != 1 || plan.Close[0].CharacterID != "assistant" {
		t.Fatalf("close plan: %+v", plan.Close)
	}
	e.ExecuteJobs(ctx, plan)

	if facts, _ := st.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}}); len(facts) != 1 {
		t.Fatalf("assistant facts: %+v", facts)
	}
	if facts, _ := st.Facts(ctx, "mute", store.FactFilter{Status: []string{"active"}}); len(facts) != 0 {
		t.Fatalf("memory-off character got facts: %+v", facts)
	}
	if _, err := st.Checkin(ctx, yesterday, "close", "mute"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("memory-off character was claimed: %v", err)
	}
	if fake.Remaining() != 0 {
		t.Fatalf("extra model calls: %d", fake.Remaining())
	}
}

func TestCloseSetsToldByFromSpeaker(t *testing.T) {
	now := local(9, 0)
	ctx := context.Background()
	st := storetest.New(t)
	today := LogicalDay(now, testLoc, 4*time.Hour)
	yesterday := today.AddDate(0, 0, -1)
	if _, err := st.CreateConversation(ctx, store.Conversation{ID: "room:abc:socrates", CharacterID: "socrates"}); err != nil {
		t.Fatal(err)
	}
	src, err := st.AddMessageAt(ctx, store.Message{
		ConversationID: "room:abc:socrates", Role: "user", Speaker: "ria",
		Content: "The user is behind on the thesis.",
	}, At(yesterday, 5*time.Hour, testLoc))
	if err != nil {
		t.Fatal(err)
	}

	fake := llm.NewFake(llm.FakeStep{Text: `{"facts":[
		{"op":"ADD","text":"is behind on the thesis","category":"work","about":"me","told_by":"assistant","source_message_id":` + strconv.FormatInt(src.ID, 10) + `}
	]}`})
	e := closeEngineFor(t, st, fake, "socrates", now)
	markClosed(t, e, today, 2, 7)

	plan, err := e.Decide(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Close) != 1 || plan.Close[0].CharacterID != "socrates" {
		t.Fatalf("close plan: %+v", plan.Close)
	}
	e.ExecuteJobs(ctx, plan)

	facts, err := st.Facts(ctx, "socrates", store.FactFilter{Status: []string{"active"}})
	if err != nil || len(facts) != 1 {
		t.Fatalf("facts: %+v, %v", facts, err)
	}
	if facts[0].ToldBy != "ria" {
		t.Fatalf("told_by came from the model instead of the speaker: %+v", facts[0])
	}
	if facts[0].About != "me" {
		t.Fatalf("about: %+v", facts[0])
	}
	prompt := fake.Requests()[0].Messages[0].Content
	if !strings.Contains(prompt, "[#"+strconv.FormatInt(src.ID, 10)+"] [05:00] ria:") {
		t.Fatalf("close prompt does not name the speaker:\n%s", prompt)
	}
}

// closeEngineFor builds a close-job engine for a persona's room day.
func closeEngineFor(t *testing.T, st *store.Store, fake *llm.Fake, charID string, now time.Time) *Engine {
	t.Helper()
	settings := func(context.Context) Settings {
		return Settings{TZ: "Asia/Makassar", Loc: testLoc, DayStartsAt: 4 * time.Hour, MorningAt: 8 * time.Hour, EveningAt: 21 * time.Hour}
	}
	runner := New(Config{
		Store: st, Tools: nil, DB: st,
		Models:   map[string]llm.Model{string(llm.OpenCodeProvider): fake},
		Now:      func() time.Time { return now },
		Location: testLoc,
		Settings: settings,
	})
	return &Engine{Store: st, Runner: runner, Now: func() time.Time { return now },
		Char: Character{ID: charID, Name: charID, Provider: "opencode", Model: "fake",
			Persona: "You are " + charID + ".", Context: "memory"},
		Setting: settings,
	}
}

func TestCloseRejectsSourcelessRoomFact(t *testing.T) {
	now := local(9, 0)
	ctx := context.Background()
	st := storetest.New(t)
	today := LogicalDay(now, testLoc, 4*time.Hour)
	yesterday := today.AddDate(0, 0, -1)
	if _, err := st.CreateConversation(ctx, store.Conversation{ID: "room:abc:socrates", CharacterID: "socrates"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMessageAt(ctx, store.Message{
		ConversationID: "room:abc:socrates", Role: "user", Speaker: "ria",
		Content: "The user is behind on the thesis.",
	}, At(yesterday, 5*time.Hour, testLoc)); err != nil {
		t.Fatal(err)
	}
	fake := llm.NewFake(llm.FakeStep{Text: `{"facts":[
		{"op":"ADD","text":"is behind on the thesis","category":"work","about":"me"}
	]}`})
	e := closeEngineFor(t, st, fake, "socrates", now)
	markClosed(t, e, today, 2, 7)

	plan, err := e.Decide(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	e.ExecuteJobs(ctx, plan)

	if facts, _ := st.Facts(ctx, "socrates", store.FactFilter{Status: []string{"active"}}); len(facts) != 0 {
		t.Fatalf("a sourceless room fact was applied: %+v", facts)
	}
	all, err := st.RunsByKind(ctx, "close")
	if err != nil || len(all) != 1 {
		t.Fatalf("runs: %+v, %v", all, err)
	}
	if !strings.Contains(string(all[0].Context), "source_message_id is required") {
		t.Fatalf("reject not recorded: %s", all[0].Context)
	}
}

func TestCloseRejectsUnknownAbout(t *testing.T) {
	now := local(9, 0)
	e, st, _ := newTestEngine(t, now, llm.FakeStep{Text: `{"facts":[
		{"op":"ADD","text":"is behind on the thesis","category":"work","about":"bob"}
	]}`})
	ctx := context.Background()
	today := LogicalDay(now, testLoc, 4*time.Hour)
	yesterday := today.AddDate(0, 0, -1)
	markClosed(t, e, today, 2, 7)
	seedDayThread(t, st, "assistant", yesterday, "I am behind on the thesis.")

	plan, _ := e.Decide(ctx, now)
	e.ExecuteJobs(ctx, plan)

	if facts, _ := st.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}}); len(facts) != 0 {
		t.Fatalf("unknown about was applied: %+v", facts)
	}
	all, err := st.RunsByKind(ctx, "close")
	if err != nil || len(all) != 1 {
		t.Fatalf("runs: %+v, %v", all, err)
	}
	if !strings.Contains(string(all[0].Context), "about") {
		t.Fatalf("reject not recorded: %s", all[0].Context)
	}
}

func TestCloseSkipsMeCopy(t *testing.T) {
	now := local(9, 0)
	e, st, fake := newTestEngine(t, now)
	e.MemoryEnabled = func(characterID string) bool { return characterID != "me" }
	ctx := context.Background()
	today := LogicalDay(now, testLoc, 4*time.Hour)
	yesterday := today.AddDate(0, 0, -1)
	markClosed(t, e, today, 2, 7)
	if _, err := st.CreateConversation(ctx, store.Conversation{ID: "room:abc:me", CharacterID: "me"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMessageAt(ctx, store.Message{
		ConversationID: "room:abc:me", Role: "user", Speaker: "me", Content: "hi all",
	}, At(yesterday, 5*time.Hour, testLoc)); err != nil {
		t.Fatal(err)
	}

	plan, err := e.Decide(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Close) != 0 {
		t.Fatalf("the me copy was claimed for closing: %+v", plan.Close)
	}
	if fake.Remaining() != 0 {
		t.Fatalf("model called for the me copy")
	}
}
