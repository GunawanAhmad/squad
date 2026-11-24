package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"characters/llm"
	"characters/store"
	"characters/store/storetest"
)

// The check-in engine is tested with a fake clock and the real store: what
// matters is the rows it writes, never a sleep.

func newTestEngine(t *testing.T, now time.Time, steps ...llm.FakeStep) (*Engine, *store.Store, *llm.Fake) {
	t.Helper()
	st := storetest.New(t)
	fake := llm.NewFake(steps...)
	runner := New(Config{
		Store: st, Tools: nil, DB: st,
		Models: map[string]llm.Model{
			string(llm.OpenCodeProvider): fake,
			string(llm.ClaudeProvider):   fake,
		},
		Now:      func() time.Time { return now },
		Location: testLoc,
		Settings: func(context.Context) Settings {
			return Settings{TZ: "Asia/Makassar", Loc: testLoc, DayStartsAt: 4 * time.Hour, MorningAt: 8 * time.Hour, EveningAt: 21 * time.Hour}
		},
	})
	engine := &Engine{
		Store: st, Runner: runner, Now: func() time.Time { return now },
		Char: Character{
			ID: "assistant", Name: "Assistant", Provider: "opencode", Model: "fake",
			Persona: "You are the assistant.", Daily: true,
		},
		Setting: func(context.Context) Settings {
			return Settings{TZ: "Asia/Makassar", Loc: testLoc, DayStartsAt: 4 * time.Hour, MorningAt: 8 * time.Hour, EveningAt: 21 * time.Hour}
		},
	}
	return engine, st, fake
}

// local builds an instant in Asia/Makassar on 1 December 2026.
func local(hour, min int) time.Time {
	return time.Date(2026, 12, 1, hour, min, 0, 0, testLoc)
}

func mustDue(t *testing.T, e *Engine, now time.Time) Plan {
	t.Helper()
	plan, err := e.Decide(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func checkinStatus(t *testing.T, st *store.Store, day time.Time, kind string) string {
	t.Helper()
	c, err := st.Checkin(context.Background(), day, kind)
	if errors.Is(err, store.ErrNotFound) {
		return "missing"
	}
	if err != nil {
		t.Fatal(err)
	}
	return c.Status
}

func assistantMessages(t *testing.T, st *store.Store, convID string) []store.Message {
	t.Helper()
	msgs, err := st.Messages(context.Background(), convID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Message
	for _, m := range msgs {
		if m.Role == "assistant" {
			out = append(out, m)
		}
	}
	return out
}

func TestDueMorningOnceWithConcurrentPolls(t *testing.T) {
	now := local(9, 0)
	e, st, _ := newTestEngine(t, now, llm.FakeStep{Text: "Morning! What do you want to do today?"})

	// Two tabs poll at the same moment.
	var plans [2]Plan
	var errs [2]error
	var wg sync.WaitGroup
	for i := range plans {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			plans[i], errs[i] = e.Decide(context.Background(), now)
		}(i)
	}
	wg.Wait()

	sent := 0
	for i, p := range plans {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if p.Checkin == CheckinMorning {
			if res := e.Execute(context.Background(), p); res.Err != nil {
				t.Fatal(res.Err)
			} else if res.Status == "sent" {
				sent++
			}
		}
	}
	if sent != 1 {
		t.Fatalf("sent by %d polls, want 1", sent)
	}
	if got := checkinStatus(t, st, plans[0].Day, CheckinMorning); got != "sent" {
		t.Fatalf("morning status = %s", got)
	}
	msgs := assistantMessages(t, st, plans[0].ConversationID)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "Morning!") {
		t.Fatalf("messages: %+v", msgs)
	}

	// A later poll the same day does nothing.
	again := mustDue(t, e, local(12, 0))
	if again.Checkin != "" || len(again.Reminders) != 0 {
		t.Fatalf("second morning: %+v", again)
	}
	if msgs := assistantMessages(t, st, again.ConversationID); len(msgs) != 1 {
		t.Fatalf("duplicate check-in: %+v", msgs)
	}
}

func TestDueMorningSkippedWhenEveningIsDue(t *testing.T) {
	now := local(21, 30)
	e, st, _ := newTestEngine(t, now, llm.FakeStep{Text: "How did today go?"})
	plan := mustDue(t, e, now)
	if plan.Checkin != CheckinEvening {
		t.Fatalf("check-in: %q", plan.Checkin)
	}
	if got := checkinStatus(t, st, plan.Day, CheckinMorning); got != "skipped" {
		t.Fatalf("morning = %s, want skipped", got)
	}
	if res := e.Execute(context.Background(), plan); res.Err != nil || res.Status != "sent" {
		t.Fatalf("execute: %+v", res)
	}
	if got := checkinStatus(t, st, plan.Day, CheckinEvening); got != "sent" {
		t.Fatalf("evening = %s", got)
	}
	// The morning cannot come back.
	if next := mustDue(t, e, local(22, 0)); next.Checkin != "" {
		t.Fatalf("second check-in: %+v", next)
	}
	if got := checkinStatus(t, st, plan.Day, CheckinMorning); got != "skipped" {
		t.Fatalf("morning after evening = %s", got)
	}
}

func TestDueMorningSkippedWhenTodayHasPlans(t *testing.T) {
	now := local(9, 0)
	e, st, _ := newTestEngine(t, now)
	day := LogicalDay(now, testLoc, 4*time.Hour)
	if _, err := st.CreateTask(context.Background(), store.Task{Title: "already told you", PlannedOn: &day}); err != nil {
		t.Fatal(err)
	}

	plan := mustDue(t, e, now)
	if plan.Checkin != "" {
		t.Fatalf("asked anyway: %+v", plan)
	}
	if got := checkinStatus(t, st, day, CheckinMorning); got != "skipped" {
		t.Fatalf("morning = %s, want skipped", got)
	}

	// The evening still happens that day.
	evening := mustDue(t, e, local(21, 0))
	if evening.Checkin != CheckinEvening {
		t.Fatalf("evening: %+v", evening)
	}
}

func TestDueFailureRetriesThenGivesUp(t *testing.T) {
	now := local(9, 0)
	e, st, _ := newTestEngine(t, now,
		llm.FakeStep{Err: errors.New("gateway down")},
		llm.FakeStep{Err: errors.New("gateway down")},
		llm.FakeStep{Err: errors.New("gateway down")},
		llm.FakeStep{Err: errors.New("gateway down")},
	)

	plan := mustDue(t, e, now)
	if plan.Checkin != CheckinMorning {
		t.Fatalf("first plan: %+v", plan)
	}
	if res := e.Execute(context.Background(), plan); res.Status != "failed" {
		t.Fatalf("execute: %+v", res)
	}
	c, err := st.Checkin(context.Background(), plan.Day, CheckinMorning)
	if err != nil || c.Status != "failed" || c.Attempts != 1 {
		t.Fatalf("after failure: %+v, %v", c, err)
	}

	// Next poll retries the same row instead of claiming a new one.
	retry := mustDue(t, e, local(9, 5))
	if retry.Checkin != CheckinMorning || !retry.CheckinRetry {
		t.Fatalf("retry plan: %+v", retry)
	}
	e.Execute(context.Background(), retry)
	e.Execute(context.Background(), mustDue(t, e, local(9, 10)))
	e.Execute(context.Background(), mustDue(t, e, local(9, 15)))
	later := mustDue(t, e, local(9, 20))
	if later.Checkin != "" {
		t.Fatalf("kept retrying after %d attempts: %+v", maxAttempts, later)
	}
}

func TestDueRemindersFireOncePerDay(t *testing.T) {
	now := local(9, 0)
	e, st, _ := newTestEngine(t, now, llm.FakeStep{Text: "Morning!"})

	// A daily reminder, and a one-off that came due while the tab was closed.
	at := local(15, 0)
	if _, err := st.AddReminder(context.Background(), store.Reminder{Text: "stretch", RemindAt: at, Repeat: "daily"}); err != nil {
		t.Fatal(err)
	}
	missedAt := local(16, 0)
	if _, err := st.AddReminder(context.Background(), store.Reminder{Text: "call mom", RemindAt: missedAt}); err != nil {
		t.Fatal(err)
	}

	plan := mustDue(t, e, local(15, 5))
	if len(plan.Reminders) != 1 || plan.Reminders[0].Missed {
		t.Fatalf("on-time reminder: %+v", plan.Reminders)
	}
	late := mustDue(t, e, local(17, 0))
	if len(late.Reminders) != 1 || !late.Reminders[0].Missed {
		t.Fatalf("missed reminder: %+v", late.Reminders)
	}

	msgs := assistantMessages(t, st, plan.ConversationID)
	if len(msgs) != 2 {
		t.Fatalf("reminder messages: %+v", msgs)
	}
	if msgs[0].Content != "⏰ stretch" {
		t.Fatalf("on-time text: %q", msgs[0].Content)
	}
	if msgs[1].Content != "⏰ missed at 16:00: call mom" {
		t.Fatalf("missed text: %q", msgs[1].Content)
	}

	// The daily reminder moved to tomorrow; the one-off is done.
	daily, err := st.Reminders(context.Background(), store.ReminderFilter{Status: []string{"pending"}})
	if err != nil || len(daily) != 1 || !daily[0].RemindAt.Equal(at.AddDate(0, 0, 1)) {
		t.Fatalf("pending after firing: %+v, %v", daily, err)
	}
	fired, err := st.Reminders(context.Background(), store.ReminderFilter{Status: []string{"fired"}})
	if err != nil || len(fired) != 1 || fired[0].Text != "call mom" {
		t.Fatalf("fired: %+v, %v", fired, err)
	}

	// Same day, later: nothing repeats.
	if again := mustDue(t, e, local(18, 0)); len(again.Reminders) != 0 {
		t.Fatalf("fired twice: %+v", again.Reminders)
	}
}

func TestDueDailyReminderAfterDaysAway(t *testing.T) {
	now := local(9, 0)
	e, st, _ := newTestEngine(t, now, llm.FakeStep{Text: "Morning!"})
	// Set three days earlier, 15:00; the tab was closed since.
	at := local(15, 0).AddDate(0, 0, -3)
	if _, err := st.AddReminder(context.Background(), store.Reminder{Text: "stand up", RemindAt: at, Repeat: "daily"}); err != nil {
		t.Fatal(err)
	}

	plan := mustDue(t, e, local(16, 0))
	if len(plan.Reminders) != 1 {
		t.Fatalf("after days away: %+v", plan.Reminders)
	}
	msgs := assistantMessages(t, st, plan.ConversationID)
	if len(msgs) != 1 {
		t.Fatalf("fired %d times, want once: %+v", len(msgs), msgs)
	}
	pending, err := st.Reminders(context.Background(), store.ReminderFilter{Status: []string{"pending"}})
	if err != nil || len(pending) != 1 || !pending[0].RemindAt.After(local(16, 0)) {
		t.Fatalf("next firing: %+v, %v", pending, err)
	}
}

func TestDueNightBelongsToThePreviousDay(t *testing.T) {
	// 02:00 on 2 December is still 1 December, so the morning check-in goes to
	// daily:2026-12-01 and a check-in for the 2nd is not due yet.
	now := local(2, 0).AddDate(0, 0, 1)
	e, st, _ := newTestEngine(t, now, llm.FakeStep{Text: "Late morning"})
	for _, d := range []time.Time{local(0, 30).AddDate(0, 0, 1), local(3, 59).AddDate(0, 0, 1)} {
		if got := LogicalDay(d, testLoc, 4*time.Hour); !got.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("LogicalDay(%s) = %v", d, got)
		}
	}

	plan := mustDue(t, e, now)
	// Whatever is due, it belongs to the previous day's thread.
	if plan.ConversationID != "daily:2026-12-01" {
		t.Fatalf("thread: %s", plan.ConversationID)
	}
	if plan.Checkin == CheckinMorning {
		t.Fatalf("a 02:00 poll asked about a day that has not started: %+v", plan)
	}
	conv, err := st.GetConversation(context.Background(), plan.ConversationID)
	if err != nil || conv.Day == nil || store.DayString(*conv.Day) != "2026-12-01" {
		t.Fatalf("conversation: %+v, %v", conv, err)
	}
	if msgs := assistantMessages(t, st, "daily:2026-12-02"); len(msgs) != 0 {
		t.Fatalf("messages in tomorrow's thread: %+v", msgs)
	}
}

func TestDueTriggerMessageIsStoredAndHidden(t *testing.T) {
	now := local(9, 0)
	e, st, _ := newTestEngine(t, now, llm.FakeStep{Text: "Morning!"})
	plan := mustDue(t, e, now)
	if res := e.Execute(context.Background(), plan); res.Err != nil {
		t.Fatal(res.Err)
	}
	msgs, err := st.Messages(context.Background(), plan.ConversationID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Role != "trigger" || !strings.Contains(msgs[0].Content, "Morning check-in") {
		t.Fatalf("stored trigger: %+v", msgs)
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "Morning!" {
		t.Fatalf("check-in message: %+v", msgs[1])
	}
	c, err := st.Checkin(context.Background(), plan.Day, CheckinMorning)
	if err != nil || c.MessageID == nil || *c.MessageID != msgs[1].ID || c.RunID == nil {
		t.Fatalf("checkin row: %+v, %v", c, err)
	}
}

func TestSkipCheckinSkipsAheadOfTime(t *testing.T) {
	now := local(7, 0)
	e, st, _ := newTestEngine(t, now)
	day := LogicalDay(now, testLoc, 4*time.Hour)
	if err := st.SkipCheckin(context.Background(), day.AddDate(0, 0, 1), CheckinMorning); err != nil {
		t.Fatal(err)
	}

	// Tomorrow morning: no check-in.
	tomorrow := local(9, 0).AddDate(0, 0, 1)
	e.Now = func() time.Time { return tomorrow }
	plan := mustDue(t, e, tomorrow)
	if plan.Checkin != "" {
		t.Fatalf("skipped morning still ran: %+v", plan)
	}
	if got := checkinStatus(t, st, LogicalDay(tomorrow, testLoc, 4*time.Hour), CheckinMorning); got != "skipped" {
		t.Fatalf("status: %s", got)
	}
}

func TestDueBusyGuard(t *testing.T) {
	e, _, _ := newTestEngine(t, local(9, 0), llm.FakeStep{Text: "one"})
	if e.Busy() {
		t.Fatal("engine busy before any turn")
	}
	e.mu.Lock()
	e.busy = true
	e.mu.Unlock()
	plan := Plan{Day: LogicalDay(local(9, 0), testLoc, 4*time.Hour), ConversationID: "daily:2026-12-01", Checkin: CheckinMorning}
	if res := e.Execute(context.Background(), plan); res.Status != "" {
		t.Fatalf("second check-in ran while busy: %+v", res)
	}
	if msg := fmt.Sprint(e.Busy()); msg != "true" {
		t.Fatalf("busy: %s", msg)
	}
}
