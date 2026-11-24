package tools_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"characters/store"
	"characters/store/storetest"
	"characters/tools"
)

func TestRemindMe(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "remind_me", "list_reminders", "cancel_reminder")

	res := runTool(t, env, "remind_me", `{"text":"call mom","at":"15:00"}`)
	if !strings.Contains(res.Text, "call mom") || !strings.Contains(res.Text, "15:00") {
		t.Fatalf("remind: %q", res.Text)
	}
	if len(res.Undo) != 1 || res.Undo[0].Op != "cancel_reminder" {
		t.Fatalf("undo: %+v", res.Undo)
	}

	// The instant is today at 15:00 in the user's zone.
	rows, err := db.Reminders(context.Background(), store.ReminderFilter{Status: []string{"pending"}})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows: %+v, %v", rows, err)
	}
	if got := rows[0].RemindAt.In(makassar).Format("2006-01-02 15:04"); got != "2026-12-01 15:00" {
		t.Fatalf("remind_at = %s", got)
	}
	if rows[0].SourceMessageID == nil || rows[0].ToolCallID == nil {
		t.Fatalf("provenance: %+v", rows[0])
	}

	runTool(t, env, "remind_me", `{"text":"stretch","at":"tomorrow 09:00","repeat":"daily"}`)
	runTool(t, env, "remind_me", `{"text":"in a bit","at":"in 30m"}`)
	if got := findReminder(t, db, "in a bit").RemindAt.In(makassar).Format("15:04"); got != "09:30" {
		t.Fatalf("relative reminder = %s", got)
	}

	listed := runTool(t, env, "list_reminders", `{}`)
	if !strings.Contains(listed.Text, "3 reminder(s)") || !strings.Contains(listed.Text, "(daily)") {
		t.Fatalf("list: %q", listed.Text)
	}

	cancelled := runTool(t, env, "cancel_reminder", `{"reminder":"call mom"}`)
	if !strings.Contains(cancelled.Text, "cancelled") || len(cancelled.Undo) != 1 || cancelled.Undo[0].Op != "restore_reminder" {
		t.Fatalf("cancel: %+v", cancelled)
	}
	pending := runTool(t, env, "list_reminders", `{}`)
	if !strings.Contains(pending.Text, "2 reminder(s)") {
		t.Fatalf("pending after cancel: %q", pending.Text)
	}

	// An exact title wins over a longer one that contains it.
	exact := runTool(t, env, "cancel_reminder", `{"reminder":"stretch"}`)
	if !strings.Contains(exact.Text, "cancelled: #2 stretch") {
		t.Fatalf("exact match: %q", exact.Text)
	}

	// Ambiguity lists candidates instead of picking one.
	runTool(t, env, "remind_me", `{"text":"stretch again","at":"18:00"}`)
	runTool(t, env, "remind_me", `{"text":"stretch once more","at":"19:00"}`)
	amb := runTool(t, env, "cancel_reminder", `{"reminder":"stretch"}`)
	if !strings.Contains(amb.Text, "ambiguous") || !strings.Contains(amb.Text, "stretch again") {
		t.Fatalf("ambiguous: %q", amb.Text)
	}
}

func findReminder(t *testing.T, db *store.Store, text string) store.Reminder {
	t.Helper()
	rows, err := db.Reminders(context.Background(), store.ReminderFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Text == text {
			return r
		}
	}
	t.Fatalf("no reminder %q", text)
	return store.Reminder{}
}

func TestReminderValidation(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "remind_me", "list_reminders", "cancel_reminder")

	cases := []struct {
		name, tool, args string
		want             error
	}{
		{"no text", "remind_me", `{"text":" ","at":"15:00"}`, tools.ErrInvalid},
		{"no time", "remind_me", `{"text":"x"}`, tools.ErrInvalid},
		{"past time", "remind_me", `{"text":"x","at":"07:00"}`, tools.ErrInvalid},
		{"yesterday", "remind_me", `{"text":"x","at":"yesterday 15:00"}`, tools.ErrInvalid},
		{"bad repeat", "remind_me", `{"text":"x","at":"15:00","repeat":"hourly"}`, tools.ErrInvalid},
		{"bad clock", "remind_me", `{"text":"x","at":"25:99"}`, tools.ErrInvalid},
		{"bad status", "list_reminders", `{"status":"soon"}`, tools.ErrInvalid},
		{"unknown id", "cancel_reminder", `{"reminder":"#999"}`, store.ErrNotFound},
		{"no ref", "cancel_reminder", `{"reminder":""}`, tools.ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := tools.New().Run(context.Background(), env, c.tool, c.args); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestNextOccurrence(t *testing.T) {
	loc := makassar
	at := time.Date(2026, 12, 1, 15, 0, 0, 0, loc) // Tuesday
	daily := store.Reminder{RemindAt: at, Repeat: "daily"}

	if got := tools.NextOccurrence(daily, at.Add(time.Minute), loc); !got.Equal(at.AddDate(0, 0, 1)) {
		t.Fatalf("next daily: %v", got)
	}
	// Three days away: the next firing is once, tomorrow, not three times.
	away := at.AddDate(0, 0, 3)
	if got := tools.NextOccurrence(daily, away, loc); !got.Equal(at.AddDate(0, 0, 4)) {
		t.Fatalf("after 3 days: %v", got)
	}

	// Weekdays: Friday 15:00 jumps to Monday 15:00.
	friday := time.Date(2026, 12, 4, 15, 0, 0, 0, loc)
	weekday := store.Reminder{RemindAt: friday, Repeat: "weekdays"}
	if got := tools.NextOccurrence(weekday, friday.Add(time.Minute), loc); got.Weekday() != time.Monday {
		t.Fatalf("weekday jump: %v", got)
	}

	once := store.Reminder{RemindAt: at}
	if got := tools.NextOccurrence(once, at.Add(time.Hour), loc); !got.Equal(at) {
		t.Fatalf("one-off: %v", got)
	}
}
