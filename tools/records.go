package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"squad/store"
)

// ErrInvalid marks input the caller should have rejected (HTTP 400).
var ErrInvalid = errors.New("invalid input")

// ErrConflict marks an edit that would clobber newer data (HTTP 409). Undo
// returns it when the record changed since the write it is undoing.
var ErrConflict = errors.New("conflict")

const (
	maxTitle       = 500
	maxDescription = 2000
	maxNotes       = 4000
)

// TaskPatch is the mutable part of a task. update_task and PATCH
// /api/assistant/tasks/{id} decode the same struct and run the same checks, so
// the model and the Today panel cannot drift apart.
type TaskPatch struct {
	Title     *string `json:"title,omitempty"`
	Notes     *string `json:"notes,omitempty"`
	Status    *string `json:"status,omitempty"`    // open | done | dropped
	PlannedOn *string `json:"plannedOn,omitempty"` // YYYY-MM-DD | today | tomorrow | "" clears
	DueOn     *string `json:"dueOn,omitempty"`
	Project   *string `json:"project,omitempty"`
}

// TaskChange is what ApplyTaskPatch did: the row after, the row before (for
// the undo record) and whether planned_on moved later.
type TaskChange struct {
	After      store.Task
	Before     store.Task
	MovedLater bool
}

func validateTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", fmt.Errorf("%w: title is required", ErrInvalid)
	}
	if len(title) > maxTitle {
		return "", fmt.Errorf("%w: title is longer than %d characters", ErrInvalid, maxTitle)
	}
	return title, nil
}

// ParseDay turns a model- or user-supplied day into a date (midnight UTC).
// Relative words are relative to base, which the caller derives from the
// logical day (04:00 boundary in settings.tz).
func ParseDay(raw string, base time.Time) (time.Time, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	base = store.Day(base)
	switch s {
	case "today", "hari ini":
		return base, nil
	case "tomorrow", "besok":
		return base.AddDate(0, 0, 1), nil
	case "yesterday", "kemarin":
		return base.AddDate(0, 0, -1), nil
	case "lusa":
		return base.AddDate(0, 0, 2), nil
	}
	if wd, ok := weekdayNames[s]; ok {
		for d := base.AddDate(0, 0, 1); ; d = d.AddDate(0, 0, 1) {
			if d.Weekday() == wd {
				return d, nil
			}
		}
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q is not a date (use YYYY-MM-DD, today, tomorrow, or a weekday)", ErrInvalid, raw)
	}
	return store.Day(t), nil
}

var weekdayNames = map[string]time.Weekday{
	"monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
	"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday, "sunday": time.Sunday,
}

// ParseReminderAt turns "15:00", "tomorrow 09:00", "in 20m" or an RFC3339
// timestamp into an instant in the user's zone.
func ParseReminderAt(raw string, now time.Time, loc *time.Location) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, fmt.Errorf("%w: at is required", ErrInvalid)
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	lower := strings.ToLower(s)
	if rest, ok := strings.CutPrefix(lower, "in "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil || d <= 0 {
			return time.Time{}, fmt.Errorf("%w: %q is not a duration", ErrInvalid, raw)
		}
		return now.Add(d), nil
	}

	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	switch {
	case strings.HasPrefix(lower, "today "):
		lower = strings.TrimPrefix(lower, "today ")
	case strings.HasPrefix(lower, "tomorrow "):
		day = day.AddDate(0, 0, 1)
		lower = strings.TrimPrefix(lower, "tomorrow ")
	case strings.HasPrefix(lower, "yesterday "):
		return time.Time{}, fmt.Errorf("%w: that time is in the past", ErrInvalid)
	}

	// "2026-12-01 15:00" carries its own day; "15:00", "3pm", "15" do not.
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, lower, loc); err == nil {
			return t, nil
		}
	}
	minute := 0
	clock := lower
	if i := strings.IndexAny(clock, ":."); i >= 0 {
		m, err := strconv.Atoi(strings.TrimSpace(clock[i+1:]))
		if err != nil {
			return time.Time{}, fmt.Errorf("%w: %q is not a time", ErrInvalid, raw)
		}
		minute = m
		clock = clock[:i]
	}
	pm := strings.HasSuffix(clock, "pm")
	clock = strings.TrimSuffix(strings.TrimSuffix(clock, "am"), "pm")
	hour, err := strconv.Atoi(strings.TrimSpace(clock))
	if err != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return time.Time{}, fmt.Errorf("%w: %q is not a time (use HH:MM)", ErrInvalid, raw)
	}
	if pm && hour < 12 {
		hour += 12
	}
	if strings.HasSuffix(lower, "am") && hour == 12 {
		hour = 0
	}
	return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc), nil
}

// FormatDay and FormatClock are the one way days and times reach the model.
func FormatDay(day time.Time) string { return store.DayString(day) }

func FormatClock(t time.Time, loc *time.Location) string { return t.In(loc).Format("15:04") }

func validateRepeat(repeat string) (string, error) {
	repeat = strings.ToLower(strings.TrimSpace(repeat))
	switch repeat {
	case "", "daily", "weekdays":
		return repeat, nil
	}
	return "", fmt.Errorf("%w: repeat must be daily or weekdays", ErrInvalid)
}

// CreateTask validates and inserts one plan.
func CreateTask(ctx context.Context, db *store.Store, title, notes, plannedOn, project string, day time.Time, toolCallID, sourceMessageID *int64) (store.Task, error) {
	title, err := validateTitle(title)
	if err != nil {
		return store.Task{}, err
	}
	if len(notes) > maxNotes {
		return store.Task{}, fmt.Errorf("%w: notes are too long", ErrInvalid)
	}
	planned := day
	if strings.TrimSpace(plannedOn) != "" {
		if planned, err = ParseDay(plannedOn, day); err != nil {
			return store.Task{}, err
		}
	}
	t := store.Task{Title: title, Notes: notes, PlannedOn: &planned, ToolCallID: toolCallID, SourceMessageID: sourceMessageID}
	if strings.TrimSpace(project) != "" {
		p, err := db.EnsureProject(ctx, project)
		if err != nil {
			return store.Task{}, err
		}
		t.ProjectID = &p.ID
	}
	return db.CreateTask(ctx, t)
}

// ApplyTaskPatch validates a patch against the stored task and writes it.
func ApplyTaskPatch(ctx context.Context, db *store.Store, id int64, p TaskPatch, now, day time.Time) (TaskChange, error) {
	before, err := db.GetTask(ctx, id)
	if err != nil {
		return TaskChange{}, err
	}
	after := before

	if p.Title != nil {
		if after.Title, err = validateTitle(*p.Title); err != nil {
			return TaskChange{}, err
		}
	}
	if p.Notes != nil {
		if len(*p.Notes) > maxNotes {
			return TaskChange{}, fmt.Errorf("%w: notes are too long", ErrInvalid)
		}
		after.Notes = *p.Notes
	}
	if p.Project != nil {
		if strings.TrimSpace(*p.Project) == "" {
			after.ProjectID = nil
		} else {
			proj, err := db.EnsureProject(ctx, *p.Project)
			if err != nil {
				return TaskChange{}, err
			}
			after.ProjectID = &proj.ID
		}
	}
	if p.PlannedOn != nil {
		if strings.TrimSpace(*p.PlannedOn) == "" {
			after.PlannedOn = nil
		} else {
			planned, err := ParseDay(*p.PlannedOn, day)
			if err != nil {
				return TaskChange{}, err
			}
			if before.PlannedOn == nil || planned.After(store.Day(*before.PlannedOn)) {
				after.TimesMoved++ // it slipped; "what keeps slipping" is a real question
			}
			after.PlannedOn = &planned
		}
	}
	if p.DueOn != nil {
		if strings.TrimSpace(*p.DueOn) == "" {
			after.DueOn = nil
		} else {
			due, err := ParseDay(*p.DueOn, day)
			if err != nil {
				return TaskChange{}, err
			}
			after.DueOn = &due
		}
	}
	if p.Status != nil {
		status := strings.ToLower(strings.TrimSpace(*p.Status))
		switch status {
		case "open", "done", "dropped":
		default:
			return TaskChange{}, fmt.Errorf("%w: status must be open, done or dropped", ErrInvalid)
		}
		after.Status = status
		if status == "done" && before.Status != "done" {
			after.CompletedAt = &now
		}
		if status != "done" {
			after.CompletedAt = nil
		}
	}
	if sameTask(before, after) {
		return TaskChange{After: after, Before: before}, nil
	}
	updated, err := db.UpdateTask(ctx, after)
	if err != nil {
		return TaskChange{}, err
	}
	return TaskChange{
		After:      updated,
		Before:     before,
		MovedLater: before.PlannedOn != nil && after.PlannedOn != nil && store.Day(*after.PlannedOn).After(store.Day(*before.PlannedOn)),
	}, nil
}

func sameTask(a, b store.Task) bool {
	return a.Title == b.Title && a.Notes == b.Notes && a.Status == b.Status &&
		samePtr(a.ProjectID, b.ProjectID) && sameDay(a.PlannedOn, b.PlannedOn) && sameDay(a.DueOn, b.DueOn)
}

func samePtr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameDay(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return store.Day(*a).Equal(store.Day(*b))
}

// ResolveTask finds a task by "#12", "12" or title. When several tasks match
// it returns them instead of guessing, so the model can ask which one.
func ResolveTask(ctx context.Context, db *store.Store, ref string) (store.Task, []store.Task, error) {
	ref = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), "#"))
	if ref == "" {
		return store.Task{}, nil, fmt.Errorf("%w: which task?", ErrInvalid)
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		t, err := db.GetTask(ctx, id)
		return t, nil, err
	}

	all, err := db.Tasks(ctx, store.TaskFilter{Order: "newest"})
	if err != nil {
		return store.Task{}, nil, err
	}
	lower := strings.ToLower(ref)
	var exact, partial, doneExact []store.Task
	for _, t := range all {
		title := strings.ToLower(t.Title)
		switch {
		case title == lower && t.Status == "open":
			exact = append(exact, t)
		case title == lower:
			doneExact = append(doneExact, t)
		case strings.Contains(title, lower) && t.Status == "open":
			partial = append(partial, t)
		}
	}
	for _, set := range [][]store.Task{exact, doneExact, partial} {
		if len(set) == 1 {
			return set[0], nil, nil
		}
		if len(set) > 1 {
			return store.Task{}, set, nil
		}
	}
	return store.Task{}, nil, fmt.Errorf("%w: no task matches %q", store.ErrNotFound, ref)
}

// TaskLine is the one-line description the model and the UI both use.
func TaskLine(t store.Task, loc *time.Location) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s", t.ID, t.Title)
	if t.PlannedOn != nil {
		fmt.Fprintf(&b, " (%s", store.DayString(*t.PlannedOn))
		if t.TimesMoved > 0 {
			fmt.Fprintf(&b, ", moved %dx", t.TimesMoved)
		}
		b.WriteString(")")
	}
	if t.Status != "open" {
		fmt.Fprintf(&b, " [%s]", t.Status)
	}
	return b.String()
}

// AddActivity validates and inserts one "what I did" row. Minutes stay nil
// when the user did not say: the assistant never guesses durations.
func AddActivity(ctx context.Context, db *store.Store, description string, minutes *int, onDay, project string, day time.Time, toolCallID, sourceMessageID *int64) (store.Activity, error) {
	description = strings.TrimSpace(description)
	if description == "" {
		return store.Activity{}, fmt.Errorf("%w: description is required", ErrInvalid)
	}
	if len(description) > maxDescription {
		return store.Activity{}, fmt.Errorf("%w: description is too long", ErrInvalid)
	}
	if minutes != nil && (*minutes < 1 || *minutes > 1440) {
		return store.Activity{}, fmt.Errorf("%w: minutes must be between 1 and 1440", ErrInvalid)
	}
	d := day
	if strings.TrimSpace(onDay) != "" {
		var err error
		if d, err = ParseDay(onDay, day); err != nil {
			return store.Activity{}, err
		}
	}
	a := store.Activity{Day: d, Description: description, Minutes: minutes, ToolCallID: toolCallID, SourceMessageID: sourceMessageID}
	if strings.TrimSpace(project) != "" {
		p, err := db.EnsureProject(ctx, project)
		if err != nil {
			return store.Activity{}, err
		}
		a.ProjectID = &p.ID
	}
	return db.AddActivity(ctx, a)
}

// AddReminder validates and inserts a reminder in the user's zone.
func AddReminder(ctx context.Context, db *store.Store, text, at, repeat string, now time.Time, loc *time.Location, toolCallID, sourceMessageID *int64) (store.Reminder, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return store.Reminder{}, fmt.Errorf("%w: text is required", ErrInvalid)
	}
	if len(text) > maxTitle {
		return store.Reminder{}, fmt.Errorf("%w: text is too long", ErrInvalid)
	}
	repeat, err := validateRepeat(repeat)
	if err != nil {
		return store.Reminder{}, err
	}
	when, err := ParseReminderAt(at, now, orUTC(loc))
	if err != nil {
		return store.Reminder{}, err
	}
	if !when.After(now) {
		return store.Reminder{}, fmt.Errorf("%w: %s is in the past", ErrInvalid, when.In(orUTC(loc)).Format("15:04"))
	}
	return db.AddReminder(ctx, store.Reminder{
		Text: text, RemindAt: when, Repeat: repeat, ToolCallID: toolCallID, SourceMessageID: sourceMessageID,
	})
}

// NextOccurrence is when a repeating reminder fires again: the first matching
// wall-clock time strictly after now, so days away fire once, not once per day.
func NextOccurrence(r store.Reminder, now time.Time, loc *time.Location) time.Time {
	loc = orUTC(loc)
	at := r.RemindAt.In(loc)
	if r.Repeat == "" {
		return at
	}
	next := at
	for {
		switch r.Repeat {
		case "weekdays":
			next = next.AddDate(0, 0, 1)
			if wd := next.Weekday(); wd == time.Saturday || wd == time.Sunday {
				continue
			}
		default:
			next = next.AddDate(0, 0, 1)
		}
		if next.After(now) {
			return next
		}
	}
}

// ReminderLine is the one-line description shared by the tool output and the UI.
func ReminderLine(r store.Reminder, loc *time.Location) string {
	line := fmt.Sprintf("#%d %s at %s", r.ID, r.Text, r.RemindAt.In(orUTC(loc)).Format("2006-01-02 15:04"))
	if r.Repeat != "" {
		line += " (" + r.Repeat + ")"
	}
	return line
}

// ActivityLine formats one logged activity.
func ActivityLine(a store.Activity) string {
	line := a.Description
	if a.Minutes != nil {
		line += fmt.Sprintf(" (%d min)", *a.Minutes)
	}
	return line
}

// UndoOp is the inverse of one write, stored in tool_calls.undo. Expect holds
// the state the write left behind: if the row changed since, undo refuses
// instead of clobbering the newer edit.
type UndoOp struct {
	Op             string          `json:"op"` // delete_task | restore_task | delete_activity | cancel_reminder | restore_reminder | unskip_checkin | forget_fact | restore_fact
	TaskID         int64           `json:"task_id,omitempty"`
	FactID         int64           `json:"fact_id,omitempty"`
	ActivityID     int64           `json:"activity_id,omitempty"`
	ReminderID     int64           `json:"reminder_id,omitempty"`
	CheckinDay     string          `json:"checkin_day,omitempty"`
	CheckinKind    string          `json:"checkin_kind,omitempty"`
	ExpectUpdated  string          `json:"expect_updated,omitempty"` // tasks: RFC3339 updated_at
	ExpectStatus   string          `json:"expect_status,omitempty"`  // reminders: status at write time
	PreviousTask   json.RawMessage `json:"previous_task,omitempty"`
	PreviousRemind json.RawMessage `json:"previous_reminder,omitempty"`
	PreviousFact   json.RawMessage `json:"previous_fact,omitempty"`
}

func encodeUndo(op UndoOp) []byte {
	raw, err := json.Marshal(op)
	if err != nil {
		return nil
	}
	return raw
}

// ApplyUndo reverses one stored write. A tool call may have recorded several
// inverse operations (plan_tasks records one per task); targetKind/ID restrict
// undo to a single record, which is what the Today panel asks for, and an empty
// target undoes the whole call, which is what the chat's undo tool does.
// It never silently overwrites: when a record moved on since the write, it
// refuses with ErrConflict and says what changed.
func ApplyUndo(ctx context.Context, db *store.Store, characterID string, toolCallID int64, targetKind string, targetID int64) (string, error) {
	tc, err := db.GetToolCall(ctx, toolCallID)
	if err != nil {
		return "", err
	}
	if tc.Status == "undone" {
		return "", fmt.Errorf("%w: tool call #%d was already undone", ErrConflict, toolCallID)
	}
	if tc.Status != "ok" {
		return "", fmt.Errorf("%w: tool call #%d did not succeed, nothing to undo", ErrConflict, toolCallID)
	}
	var ops []UndoOp
	if len(tc.Undo) == 0 || string(tc.Undo) == "null" {
		return "", fmt.Errorf("%w: tool call #%d changed nothing", ErrConflict, toolCallID)
	}
	if err := json.Unmarshal(tc.Undo, &ops); err != nil {
		return "", fmt.Errorf("tool call %d: bad undo record: %w", toolCallID, err)
	}

	selected := ops
	if targetKind != "" {
		selected = nil
		for _, op := range ops {
			kind, id := op.target()
			if kind == targetKind && (targetID == 0 || id == targetID) {
				selected = append(selected, op)
			}
		}
		if len(selected) == 0 {
			return "", fmt.Errorf("%w: tool call #%d did not write that %s", ErrConflict, toolCallID, targetKind)
		}
	}

	var summaries []string
	remaining := append([]UndoOp(nil), ops...)
	for _, op := range selected {
		summary, err := applyUndoOp(ctx, db, characterID, op)
		if err != nil {
			// Keep the ops that already succeeded out of the stored record so
			// a retry does not try to apply them a second time and conflict.
			if len(summaries) > 0 {
				if raw, mErr := json.Marshal(remaining); mErr == nil {
					_ = db.FinishToolCall(context.WithoutCancel(ctx), toolCallID, tc.Result, raw, "ok")
				}
			}
			return "", err
		}
		summaries = append(summaries, summary)
		remaining = removeOp(remaining, op)
	}
	if len(remaining) == 0 {
		if err := db.SetToolCallStatus(ctx, toolCallID, "undone"); err != nil {
			return "", err
		}
	} else {
		raw, _ := json.Marshal(remaining)
		if err := db.FinishToolCall(ctx, toolCallID, tc.Result, raw, "ok"); err != nil {
			return "", err
		}
	}
	return strings.Join(summaries, "; "), nil
}

func (op UndoOp) target() (string, int64) {
	switch op.Op {
	case "delete_task", "restore_task":
		return "task", op.TaskID
	case "delete_activity":
		return "activity", op.ActivityID
	case "cancel_reminder", "restore_reminder":
		return "reminder", op.ReminderID
	case "unskip_checkin":
		return "checkin", 0
	case "forget_fact", "restore_fact":
		return "fact", op.FactID
	}
	return "", 0
}

func removeOp(ops []UndoOp, op UndoOp) []UndoOp {
	for i, o := range ops {
		if o.Op == op.Op && o.TaskID == op.TaskID && o.ActivityID == op.ActivityID &&
			o.ReminderID == op.ReminderID && o.FactID == op.FactID &&
			o.CheckinDay == op.CheckinDay && o.CheckinKind == op.CheckinKind {
			return append(append([]UndoOp(nil), ops[:i]...), ops[i+1:]...)
		}
	}
	return ops
}

func applyUndoOp(ctx context.Context, db *store.Store, characterID string, op UndoOp) (string, error) {
	switch op.Op {
	case "delete_task":
		t, err := db.GetTask(ctx, op.TaskID)
		if err != nil {
			return "", fmt.Errorf("%w: task #%d is already gone", ErrConflict, op.TaskID)
		}
		if err := expectTaskState(t, op); err != nil {
			return "", err
		}
		if err := db.DeleteTask(ctx, op.TaskID); err != nil {
			return "", err
		}
		return fmt.Sprintf("removed plan #%d %q", t.ID, t.Title), nil

	case "restore_task":
		var prev store.Task
		if err := json.Unmarshal(op.PreviousTask, &prev); err != nil {
			return "", fmt.Errorf("bad task snapshot: %w", err)
		}
		t, err := db.GetTask(ctx, prev.ID)
		if err != nil {
			return "", fmt.Errorf("%w: task #%d is already gone", ErrConflict, prev.ID)
		}
		if err := expectTaskState(t, op); err != nil {
			return "", err
		}
		if _, err := db.UpdateTask(ctx, prev); err != nil {
			return "", err
		}
		return fmt.Sprintf("restored plan #%d %q (%s)", prev.ID, prev.Title, prev.Status), nil

	case "delete_activity":
		a, err := db.GetActivity(ctx, op.ActivityID)
		if err != nil {
			return "", fmt.Errorf("%w: logged item #%d is already gone", ErrConflict, op.ActivityID)
		}
		if err := db.DeleteActivity(ctx, op.ActivityID); err != nil {
			return "", err
		}
		return fmt.Sprintf("removed log entry #%d %q", a.ID, a.Description), nil

	case "cancel_reminder":
		r, err := db.GetReminder(ctx, op.ReminderID)
		if err != nil {
			return "", fmt.Errorf("%w: reminder #%d is already gone", ErrConflict, op.ReminderID)
		}
		if r.Status != "pending" {
			return "", fmt.Errorf("%w: reminder #%d is %s, not pending", ErrConflict, r.ID, r.Status)
		}
		if err := db.DeleteReminder(ctx, r.ID); err != nil {
			return "", err
		}
		return fmt.Sprintf("removed reminder #%d %q", r.ID, r.Text), nil

	case "restore_reminder":
		var prev store.Reminder
		if err := json.Unmarshal(op.PreviousRemind, &prev); err != nil {
			return "", fmt.Errorf("bad reminder snapshot: %w", err)
		}
		current, err := db.GetReminder(ctx, prev.ID)
		if err != nil {
			return "", fmt.Errorf("%w: reminder #%d is already gone", ErrConflict, prev.ID)
		}
		if op.ExpectStatus != "" && current.Status != op.ExpectStatus {
			return "", fmt.Errorf("%w: reminder #%d is %s now, not %s; not undoing", ErrConflict, current.ID, current.Status, op.ExpectStatus)
		}
		if _, err := db.UpdateReminder(ctx, prev); err != nil {
			return "", err
		}
		return fmt.Sprintf("restored reminder #%d %q", prev.ID, prev.Text), nil

	case "forget_fact":
		f, err := db.GetFact(ctx, characterID, op.FactID)
		if err != nil {
			return "", fmt.Errorf("%w: fact #%d is already gone", ErrConflict, op.FactID)
		}
		if err := expectFactState(f, op); err != nil {
			return "", err
		}
		if err := db.ForgetFact(ctx, characterID, f.ID, time.Now()); err != nil {
			return "", err
		}
		return fmt.Sprintf("removed fact #%d %q", f.ID, f.Text), nil

	case "restore_fact":
		var prev store.Fact
		if err := json.Unmarshal(op.PreviousFact, &prev); err != nil {
			return "", fmt.Errorf("bad fact snapshot: %w", err)
		}
		if prev.CharacterID != characterID {
			return "", fmt.Errorf("%w: fact #%d belongs to another agent", ErrConflict, prev.ID)
		}
		current, err := db.GetFact(ctx, characterID, prev.ID)
		if err != nil {
			return "", fmt.Errorf("%w: fact #%d is already gone", ErrConflict, prev.ID)
		}
		if err := expectFactState(current, op); err != nil {
			return "", err
		}
		if _, err := db.UpdateFact(ctx, prev); err != nil {
			return "", err
		}
		return fmt.Sprintf("restored fact #%d %q (%s)", prev.ID, prev.Text, prev.Status), nil

	case "unskip_checkin":
		day, err := time.Parse("2006-01-02", op.CheckinDay)
		if err != nil {
			return "", fmt.Errorf("bad check-in day: %w", err)
		}
		c, err := db.Checkin(ctx, day, op.CheckinKind, characterID)
		if err != nil || c.Status != "skipped" {
			return "", fmt.Errorf("%w: that check-in is not skipped any more", ErrConflict)
		}
		if err := db.DeleteCheckin(ctx, day, op.CheckinKind, characterID); err != nil {
			return "", err
		}
		return fmt.Sprintf("re-enabled the %s check-in on %s", op.CheckinKind, op.CheckinDay), nil
	}
	return "", fmt.Errorf("%w: unknown undo operation %q", ErrConflict, op.Op)
}

func expectFactState(f store.Fact, op UndoOp) error {
	if op.ExpectUpdated == "" {
		return nil
	}
	want, err := time.Parse(time.RFC3339Nano, op.ExpectUpdated)
	if err != nil {
		return fmt.Errorf("undo: bad expected time: %w", err)
	}
	if !f.UpdatedAt.Truncate(time.Microsecond).Equal(want.Truncate(time.Microsecond)) {
		return fmt.Errorf("%w: fact #%d changed since; not undoing", ErrConflict, f.ID)
	}
	return nil
}

func expectTaskState(t store.Task, op UndoOp) error {
	if op.ExpectUpdated == "" {
		return nil
	}
	want, err := time.Parse(time.RFC3339Nano, op.ExpectUpdated)
	if err != nil {
		return fmt.Errorf("undo: bad expected time: %w", err)
	}
	if !t.UpdatedAt.Truncate(time.Microsecond).Equal(want.Truncate(time.Microsecond)) {
		return fmt.Errorf("%w: task #%d changed since (it is now %s); not undoing", ErrConflict, t.ID, t.Status)
	}
	return nil
}

func orUTC(loc *time.Location) *time.Location {
	if loc == nil {
		return time.UTC
	}
	return loc
}
