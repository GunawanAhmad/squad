package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"characters/store"
	"characters/store/storetest"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestTasks(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()

	proj, err := s.EnsureProject(ctx, "API refactor")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.EnsureProject(ctx, "api refactor") // unique on lower(name)
	if err != nil || again.ID != proj.ID || again.Name != "API refactor" {
		t.Fatalf("project upsert: %+v, %v", again, err)
	}

	planned := day(2026, 12, 1)
	due := day(2026, 12, 5)
	today := day(2026, 12, 2)
	done := time.Date(2026, 12, 1, 14, 0, 0, 0, time.UTC)

	t1, err := s.CreateTask(ctx, store.Task{Title: "write tests", PlannedOn: &planned, ProjectID: &proj.ID, DueOn: &due})
	if err != nil {
		t.Fatal(err)
	}
	if t1.Status != "open" || t1.TimesMoved != 0 || t1.UpdatedAt.IsZero() {
		t.Fatalf("created task: %+v", t1)
	}
	t2, err := s.CreateTask(ctx, store.Task{Title: "ship it", PlannedOn: &today})
	if err != nil {
		t.Fatal(err)
	}
	t3, err := s.CreateTask(ctx, store.Task{Title: "old thing", PlannedOn: &planned, Status: "done", CompletedAt: &done})
	if err != nil {
		t.Fatal(err)
	}

	// Move t1 later: the store does not bump times_moved, the caller decides.
	t1.PlannedOn = &today
	t1.TimesMoved++
	t1, err = s.UpdateTask(ctx, t1)
	if err != nil {
		t.Fatal(err)
	}
	if t1.TimesMoved != 1 || !t1.PlannedOn.Equal(today) {
		t.Fatalf("updated task: %+v", t1)
	}
	if !t1.UpdatedAt.After(t1.CreatedAt) && !t1.UpdatedAt.Equal(t1.CreatedAt) {
		t.Fatalf("updated_at not bumped: %+v", t1)
	}

	got, err := s.GetTask(ctx, t1.ID)
	if err != nil || got.Title != "write tests" || got.ProjectID == nil || *got.ProjectID != proj.ID {
		t.Fatalf("get task: %+v, %v", got, err)
	}
	if _, err := s.GetTask(ctx, 9999); err != store.ErrNotFound {
		t.Fatalf("missing task: %v", err)
	}

	openToday, err := s.Tasks(ctx, store.TaskFilter{Status: []string{"open"}, PlannedOn: &today})
	if err != nil {
		t.Fatal(err)
	}
	if len(openToday) != 2 {
		t.Fatalf("open today: %+v", openToday)
	}
	earlier, err := s.Tasks(ctx, store.TaskFilter{
		Status: []string{"open"}, PlannedBefore: &today, Order: "moved", Limit: 15,
	})
	if err != nil || len(earlier) != 0 {
		t.Fatalf("open before today: %+v, %v", earlier, err)
	}
	completed, err := s.Tasks(ctx, store.TaskFilter{
		CompletedFrom: ptr(time.Date(2026, 12, 1, 4, 0, 0, 0, time.UTC)),
		CompletedTo:   ptr(time.Date(2026, 12, 2, 4, 0, 0, 0, time.UTC)),
	})
	if err != nil || len(completed) != 1 || completed[0].ID != t3.ID {
		t.Fatalf("completed in range: %+v, %v", completed, err)
	}

	if err := s.DeleteTask(ctx, t2.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTask(ctx, t2.ID); err != store.ErrNotFound {
		t.Fatalf("delete twice: %v", err)
	}
	all, err := s.Tasks(ctx, store.TaskFilter{Order: "newest"})
	if err != nil || len(all) != 2 {
		t.Fatalf("after delete: %+v, %v", all, err)
	}
}

func TestActivities(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()

	minutes := 120
	a1, err := s.AddActivity(ctx, store.Activity{Day: day(2026, 12, 1), Description: "api refactor", Minutes: &minutes})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := s.AddActivity(ctx, store.Activity{Day: day(2026, 12, 2), Description: "gym"})
	if err != nil {
		t.Fatal(err)
	}
	if a2.Minutes != nil {
		t.Fatalf("minutes should stay NULL: %+v", a2)
	}

	got, err := s.Activities(ctx, store.ActivityFilter{From: ptr(day(2026, 12, 1)), To: ptr(day(2026, 12, 1))})
	if err != nil || len(got) != 1 || got[0].ID != a1.ID {
		t.Fatalf("activities by day: %+v, %v", got, err)
	}

	a1.Description, a1.Minutes = "api refactor, done", nil
	if _, err := s.UpdateActivity(ctx, a1); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteActivity(ctx, a2.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.Activities(ctx, store.ActivityFilter{})
	if err != nil || len(got) != 1 || got[0].Description != "api refactor, done" || got[0].Minutes != nil {
		t.Fatalf("after edit: %+v, %v", got, err)
	}
}

func TestReminders(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()

	at := time.Date(2026, 12, 1, 15, 0, 0, 0, time.UTC)
	r1, err := s.AddReminder(ctx, store.Reminder{Text: "call mom", RemindAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if r1.Status != "pending" || r1.Repeat != "" || r1.FiredAt != nil {
		t.Fatalf("created reminder: %+v", r1)
	}
	r2, err := s.AddReminder(ctx, store.Reminder{Text: "stretch", RemindAt: at.Add(-time.Hour), Repeat: "daily"})
	if err != nil {
		t.Fatal(err)
	}

	due, err := s.Reminders(ctx, store.ReminderFilter{Status: []string{"pending"}, DueBefore: ptr(at)})
	if err != nil || len(due) != 2 {
		t.Fatalf("due reminders: %+v, %v", due, err)
	}

	next := at.Add(24 * time.Hour)
	r2.RemindAt, r2.FiredAt = next, &at
	r2.Status = "pending"
	if _, err := s.UpdateReminder(ctx, r2); err != nil {
		t.Fatal(err)
	}
	r1.Status, r1.FiredAt = "fired", &at
	if _, err := s.UpdateReminder(ctx, r1); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetReminder(ctx, r2.ID)
	if err != nil || !got.RemindAt.Equal(next) || got.Repeat != "daily" {
		t.Fatalf("repeating reminder: %+v, %v", got, err)
	}
	if err := s.DeleteReminder(ctx, r2.ID); err != nil {
		t.Fatal(err)
	}
}

func TestFireReminder(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 12, 1, 7, 0, 0, 0, time.UTC)
	now := at.Add(time.Minute)

	r, err := s.AddReminder(ctx, store.Reminder{Text: "stretch", RemindAt: at, Repeat: "daily"})
	if err != nil {
		t.Fatal(err)
	}
	next := at.AddDate(0, 0, 1)
	ok, err := s.FireReminder(ctx, r.ID, now, &next)
	if err != nil || !ok {
		t.Fatalf("fire: %v, %v", ok, err)
	}
	got, _ := s.GetReminder(ctx, r.ID)
	if got.Status != "pending" || !got.RemindAt.Equal(next) || got.FiredAt == nil {
		t.Fatalf("repeating reminder after firing: %+v", got)
	}
	// A second poll at the same time must not fire it again.
	if ok, _ := s.FireReminder(ctx, r.ID, now, &next); ok {
		t.Fatal("fired twice")
	}

	once, err := s.AddReminder(ctx, store.Reminder{Text: "call mom", RemindAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.FireReminder(ctx, once.ID, now, nil); err != nil || !ok {
		t.Fatalf("fire once: %v, %v", ok, err)
	}
	if got, _ := s.GetReminder(ctx, once.ID); got.Status != "fired" || got.FiredAt == nil {
		t.Fatalf("one-off after firing: %+v", got)
	}
	// A reminder that is not due yet is not claimed.
	later, err := s.AddReminder(ctx, store.Reminder{Text: "later", RemindAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.FireReminder(ctx, later.ID, now, nil); ok {
		t.Fatal("fired a future reminder")
	}
}

func TestCheckinsAndSettings(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	d := day(2026, 12, 1)

	ok, err := s.ClaimCheckin(ctx, d, "morning")
	if err != nil || !ok {
		t.Fatalf("first claim: %v, %v", ok, err)
	}
	ok, err = s.ClaimCheckin(ctx, d, "morning")
	if err != nil || ok {
		t.Fatalf("second claim must lose the race: %v, %v", ok, err)
	}
	c, err := s.Checkin(ctx, d, "morning")
	if err != nil || c.Status != "pending" || c.Attempts != 0 {
		t.Fatalf("claimed check-in: %+v, %v", c, err)
	}

	if n, err := s.FailCheckin(ctx, d, "morning"); err != nil || n != 1 {
		t.Fatalf("fail: %d, %v", n, err)
	}
	if n, err := s.FailCheckin(ctx, d, "morning"); err != nil || n != 2 {
		t.Fatalf("fail again: %d, %v", n, err)
	}
	if _, err := s.CreateConversation(ctx, store.Conversation{ID: "c", CharacterID: "assistant"}); err != nil {
		t.Fatal(err)
	}
	msg, err := s.AddMessage(ctx, store.Message{ConversationID: "c", Role: "assistant", Content: "morning"})
	if err != nil {
		t.Fatal(err)
	}
	conv := "c"
	runID, err := s.StartRun(ctx, store.Run{Kind: "checkin", ConversationID: &conv, Model: "fake", Status: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishCheckin(ctx, d, "morning", "sent", &msg.ID, &runID); err != nil {
		t.Fatal(err)
	}
	c, err = s.Checkin(ctx, d, "morning")
	if err != nil || c.Status != "sent" || c.Attempts != 2 || *c.MessageID != msg.ID || *c.RunID != runID {
		t.Fatalf("sent check-in: %+v, %v", c, err)
	}

	if err := s.SkipCheckin(ctx, d, "evening"); err != nil {
		t.Fatal(err)
	}
	if err := s.SkipCheckin(ctx, d, "evening"); err != nil { // idempotent
		t.Fatal(err)
	}
	c, err = s.Checkin(ctx, d, "evening")
	if err != nil || c.Status != "skipped" {
		t.Fatalf("skipped check-in: %+v, %v", c, err)
	}
	if _, err := s.Checkin(ctx, d, "close"); err != store.ErrNotFound {
		t.Fatalf("missing check-in: %v", err)
	}

	if err := s.SetSettings(ctx, map[string]string{"tz": "Asia/Makassar", "morning_at": "08:00"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSettings(ctx, map[string]string{"morning_at": "07:30"}); err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings(ctx)
	if err != nil || settings["tz"] != "Asia/Makassar" || settings["morning_at"] != "07:30" {
		t.Fatalf("settings: %+v, %v", settings, err)
	}
}

func TestToolCallUndoPayload(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()

	if _, err := s.CreateConversation(ctx, store.Conversation{ID: "c", CharacterID: "assistant"}); err != nil {
		t.Fatal(err)
	}
	conv := "c"
	runID, err := s.StartRun(ctx, store.Run{Kind: "turn", ConversationID: &conv, Model: "fake", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}

	tc, err := s.StartToolCall(ctx, store.ToolCall{RunID: runID, Name: "plan_tasks", Args: []byte(`{"tasks":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if tc.Status != "running" || tc.ID == 0 {
		t.Fatalf("started call: %+v", tc)
	}
	if err := s.FinishToolCall(ctx, tc.ID, "planned 1", []byte(`{"op":"delete_task","id":1}`), "ok"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetToolCall(ctx, tc.ID)
	var undo map[string]any
	_ = json.Unmarshal(got.Undo, &undo)
	if err != nil || got.Status != "ok" || got.Result != "planned 1" || undo["op"] != "delete_task" {
		t.Fatalf("finished call: %+v, %v", got, err)
	}
	if err := s.SetToolCallStatus(ctx, tc.ID, "undone"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetToolCall(ctx, tc.ID); got.Status != "undone" {
		t.Fatalf("status: %+v", got)
	}
	if _, err := s.GetToolCall(ctx, 9999); err != store.ErrNotFound {
		t.Fatalf("missing call: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
