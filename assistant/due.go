package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"characters/store"
	"characters/tools"
)

// The check-in engine is lazy: the UI's 60 s poll calls Decide, which is pure
// database work (claim today's check-in, write due reminders into the thread)
// and returns immediately. Execute runs the model turn in the background, one
// at a time. Because every decision is stored, a missed poll, a crashed server
// or two open tabs cannot double-send a check-in.

const (
	CheckinMorning = "morning"
	CheckinEvening = "evening"

	// maxAttempts stops a check-in that keeps failing from retrying all day.
	maxAttempts = 3
	// missedAfter marks a reminder written late as "missed".
	missedAfter = 5 * time.Minute

	checkinTimeout = 3 * time.Minute
)

// Engine decides what is due and runs the check-in turns.
type Engine struct {
	Store   *store.Store
	Runner  *Runner
	Char    Character // the assistant
	Now     func() time.Time
	Setting func(ctx context.Context) Settings
	// Before is the lazy daily backup: first Due call of the day.
	Before func(ctx context.Context, now time.Time)
	// Window is the raw message window the summary keeps; 0 means DefaultWindow.
	Window int

	mu   sync.Mutex
	busy bool
	jobs jobState
}

func (e *Engine) settings(ctx context.Context) Settings {
	if e.Setting != nil {
		return e.Setting(ctx)
	}
	return DefaultSettings()
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return Now()
}

// FiredReminder is one reminder written into today's thread.
type FiredReminder struct {
	ID     int64
	Text   string
	At     time.Time
	Missed bool
}

// Plan is what Decide did: reminders fired now, and the check-in (if any) that
// Execute should run.
type Plan struct {
	Day            time.Time
	ConversationID string
	Reminders      []FiredReminder
	Checkin        string     // morning | evening | ""
	CheckinRetry   bool       // a pending/failed row is being retried
	Close          []CloseJob // past (day, character) pairs whose facts still need extracting
	Summary        bool       // today's thread outgrew the window
}

// CheckinResult is the outcome of Execute.
type CheckinResult struct {
	Checkin   string
	Status    string // sent | failed | ""
	MessageID int64
	RunID     int64
	Err       error
}

// Decide does the database half: fires due reminders, marks a missed morning
// as skipped once the evening is due, and claims the check-in. It never calls
// a model.
func (e *Engine) Decide(ctx context.Context, now time.Time) (Plan, error) {
	if e.Store == nil {
		return Plan{}, errors.New("check-in engine: no store")
	}
	if e.Before != nil {
		e.Before(ctx, now)
	}
	set := e.settings(ctx)
	day := LogicalDay(now, set.Loc, set.DayStartsAt)
	plan := Plan{Day: day, ConversationID: DailyConversationID(day)}

	conv, err := e.Store.CreateConversation(ctx, store.Conversation{
		ID: plan.ConversationID, CharacterID: e.Char.ID, Day: &day,
	})
	if err != nil {
		return plan, err
	}
	if conv.CharacterID != e.Char.ID {
		return plan, fmt.Errorf("conversation %s belongs to character %s", conv.ID, conv.CharacterID)
	}

	if plan.Reminders, err = e.fireReminders(ctx, now, set, plan.ConversationID); err != nil {
		return plan, err
	}

	plan.Checkin, plan.CheckinRetry, err = e.pickCheckin(ctx, day, now, set)
	if err != nil {
		return plan, err
	}
	plan.Close, err = e.closeJobs(ctx, day, set)
	if err != nil {
		return plan, err
	}
	plan.Summary = e.summaryNeeded(ctx, day, set)
	return plan, err
}

// DailyConversationID is the one thread per logical day (D5).
func DailyConversationID(day time.Time) string { return "daily:" + store.DayString(day) }

func (e *Engine) fireReminders(ctx context.Context, now time.Time, set Settings, convID string) ([]FiredReminder, error) {
	due, err := e.Store.Reminders(ctx, store.ReminderFilter{Status: []string{"pending"}, DueBefore: &now})
	if err != nil {
		return nil, err
	}
	var fired []FiredReminder
	for _, r := range due {
		var next *time.Time
		if r.Repeat != "" {
			n := tools.NextOccurrence(r, now, set.Loc)
			next = &n
		}
		ok, err := e.Store.FireReminder(ctx, r.ID, now, next)
		if err != nil {
			return fired, err
		}
		if !ok {
			continue // another poll fired it first
		}
		missed := now.Sub(r.RemindAt) > missedAfter
		text := "⏰ " + r.Text
		if missed {
			text = fmt.Sprintf("⏰ missed at %s: %s", tools.FormatClock(r.RemindAt, set.Loc), r.Text)
		}
		if _, err := e.Store.AddMessage(ctx, store.Message{
			ConversationID: convID, Role: "assistant", Content: text,
		}); err != nil {
			return fired, err
		}
		fired = append(fired, FiredReminder{ID: r.ID, Text: r.Text, At: r.RemindAt, Missed: missed})
	}
	return fired, nil
}

// pickCheckin is the order from the plan: evening first (a missed morning is
// skipped), then morning, which is skipped when the day already has plans.
func (e *Engine) pickCheckin(ctx context.Context, day, now time.Time, set Settings) (kind string, retry bool, err error) {
	eveningAt := At(day, set.EveningAt, set.Loc)
	morningAt := At(day, set.MorningAt, set.Loc)

	if !now.Before(eveningAt) {
		ok, retry, err := e.claim(ctx, day, CheckinEvening)
		if err != nil {
			return "", false, err
		}
		if !ok {
			return "", false, nil
		}
		// The morning cannot arrive any more: record that it was skipped.
		if err := e.Store.SkipCheckin(ctx, day, CheckinMorning, e.Char.ID); err != nil {
			return "", false, err
		}
		return CheckinEvening, retry, nil
	}

	if now.Before(morningAt) {
		return "", false, nil
	}

	planned, err := e.Store.Tasks(ctx, store.TaskFilter{PlannedOn: &day, Limit: 1})
	if err != nil {
		return "", false, err
	}
	if len(planned) > 0 {
		// They already told us what today is about; don't ask again.
		return "", false, e.Store.SkipCheckin(ctx, day, CheckinMorning, e.Char.ID)
	}

	ok, retry, err := e.claim(ctx, day, CheckinMorning)
	if err != nil || !ok {
		return "", false, err
	}
	return CheckinMorning, retry, nil
}

// claim decides whether a check-in may run now. A missing row is claimed
// atomically, so two tabs polling cannot both send it.
func (e *Engine) claim(ctx context.Context, day time.Time, kind string) (ok, retry bool, err error) {
	c, err := e.Store.Checkin(ctx, day, kind, e.Char.ID)
	if errors.Is(err, store.ErrNotFound) {
		claimed, err := e.Store.ClaimCheckin(ctx, day, kind, e.Char.ID)
		return claimed, false, err
	}
	if err != nil {
		return false, false, err
	}
	switch c.Status {
	case "sent", "skipped":
		return false, false, nil
	case "pending":
		return true, true, nil
	case "failed":
		return c.Attempts < maxAttempts, true, nil
	}
	return false, false, nil
}

// Busy reports whether a check-in turn is running.
func (e *Engine) Busy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.busy
}

// Execute runs the planned check-in turn. It is safe to call from several
// polls: only one runs at a time, and the row's status makes the outcome
// visible to the next poll.
func (e *Engine) Execute(ctx context.Context, plan Plan) CheckinResult {
	if plan.Checkin == "" || e.Runner == nil {
		return CheckinResult{}
	}
	if !e.acquire() {
		return CheckinResult{}
	}
	defer e.release()

	// Another poll may have finished this check-in while this plan waited.
	if c, err := e.Store.Checkin(ctx, plan.Day, plan.Checkin, e.Char.ID); err == nil {
		if c.Status == "sent" || c.Status == "skipped" {
			return CheckinResult{}
		}
	}

	ctx, cancel := context.WithTimeout(ctx, checkinTimeout)
	defer cancel()

	kind := plan.Checkin
	res, err := e.Runner.Turn(ctx, TurnRequest{
		ConversationID: plan.ConversationID,
		Character:      e.Char,
		Input:          TriggerInstruction(kind),
		Role:           "trigger",
		Kind:           "checkin",
	}, nil)

	out := CheckinResult{Checkin: kind, Status: "sent", MessageID: res.AssistantMessageID, RunID: res.RunID}
	if err != nil {
		out.Status, out.Err = "failed", err
		if attempts, ferr := e.Store.FailCheckin(context.WithoutCancel(ctx), plan.Day, kind, e.Char.ID); ferr != nil {
			log.Printf("check-in %s %s: recording failure: %v", store.DayString(plan.Day), kind, ferr)
		} else if attempts >= maxAttempts {
			log.Printf("check-in %s %s: giving up after %d attempts: %v", store.DayString(plan.Day), kind, attempts, err)
		}
		return out
	}
	var msgID *int64
	if out.MessageID != 0 {
		msgID = &out.MessageID
	}
	var runID *int64
	if out.RunID != 0 {
		runID = &out.RunID
	}
	if err := e.Store.FinishCheckin(context.WithoutCancel(ctx), plan.Day, kind, e.Char.ID, "sent", msgID, runID); err != nil {
		log.Printf("check-in %s %s: marking sent: %v", store.DayString(plan.Day), kind, err)
	}
	return out
}

func (e *Engine) acquire() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.busy {
		return false
	}
	e.busy = true
	return true
}

func (e *Engine) release() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.busy = false
}

// TriggerInstruction is stored as a trigger message: it is what the model is
// asked to do, never shown in the UI. The eval uses the same text. Both instructions only allow questions
// and reflecting back (D6); advice happens only when the user asks.
func TriggerInstruction(kind string) string {
	switch kind {
	case CheckinEvening:
		return strings.TrimSpace(`Evening check-in. Today's plans and what was logged are in <today>.
Ask how today went, naming the plans still open in one short sentence.
If <today> lists facts you noted today, list them in one line as "I noted: …" and ask if they are right.
Use complete_task / log_activity / update_task to record what the user answers, and confirm what you recorded in one line.
Only ask questions and reflect back what the user said. Do not give advice, tips or encouragement.
If they answer nothing, accept it and stop; do not repeat the question.`)
	default:
		return strings.TrimSpace(`Morning check-in. Yesterday's and still-open plans are in <today> and <recent_days>.
If there are plans from earlier days, reflect one or two back ("yesterday you planned X; it's still open").
Then ask what the user wants to get done today, in one question.
Use plan_tasks when they answer, and confirm what you recorded in one line.
Only ask and reflect. Do not give advice, tips or encouragement. If they say they don't know, accept that; do not push.`)
	}
}
