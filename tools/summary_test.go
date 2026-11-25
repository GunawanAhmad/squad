package tools_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"characters/store"
	"characters/store/storetest"
	"characters/tools"
)

func TestGetSummary(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "get_summary")
	ctx := context.Background()

	work, err := db.EnsureProject(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	home, err := db.EnsureProject(ctx, "home")
	if err != nil {
		t.Fatal(err)
	}
	add := func(desc string, minutes int, day time.Time, project *int64) {
		t.Helper()
		m := minutes
		if _, err := db.AddActivity(ctx, store.Activity{
			Description: desc, Minutes: &m, Day: day, ProjectID: project,
		}); err != nil {
			t.Fatal(err)
		}
	}
	add("monday work", 60, env.Day, &work.ID)
	add("sunday work", 30, env.Day.AddDate(0, 0, -1), &work.ID)
	add("old home", 15, env.Day.AddDate(0, 0, -6), &home.ID)
	add("outside", 999, env.Day.AddDate(0, 0, -30), &work.ID)
	// An activity with no minutes still makes the day "logged".
	if _, err := db.AddActivity(ctx, store.Activity{
		Description: "walk", Day: env.Day.AddDate(0, 0, -3), ProjectID: &home.ID,
	}); err != nil {
		t.Fatal(err)
	}

	addTask := func(title string, planned time.Time, status string, moved int) {
		t.Helper()
		if _, err := db.CreateTask(ctx, store.Task{
			Title: title, Status: status, PlannedOn: &planned, TimesMoved: moved,
		}); err != nil {
			t.Fatal(err)
		}
	}
	addTask("done plan", env.Day, "done", 0)
	addTask("dropped plan", env.Day.AddDate(0, 0, -2), "dropped", 0)
	addTask("moved plan", env.Day.AddDate(0, 0, -5), "open", 2)
	addTask("open plan", env.Day.AddDate(0, 0, -1), "open", 0)
	addTask("outside plan", env.Day.AddDate(0, 0, 10), "open", 0)
	// Completion is counted by completed_at, even when the plan was dated
	// before the period (the digest uses the same rule).
	doneAt := time.Date(2026, 12, 1, 10, 0, 0, 0, makassar)
	doneList, err := db.Tasks(ctx, store.TaskFilter{Status: []string{"done"}})
	if err != nil || len(doneList) != 1 {
		t.Fatalf("done tasks: %+v, %v", doneList, err)
	}
	doneTask := doneList[0]
	doneTask.CompletedAt = &doneAt
	if _, err := db.UpdateTask(ctx, doneTask); err != nil {
		t.Fatal(err)
	}
	oldDone := store.Task{Title: "finished before", Status: "done", PlannedOn: ptrDay(env.Day.AddDate(0, 0, -20)), CompletedAt: &doneAt}
	if _, err := db.CreateTask(ctx, oldDone); err != nil {
		t.Fatal(err)
	}

	res := runTool(t, env, "get_summary", `{"from":"2026-11-25","to":"2026-12-01"}`)
	for _, want := range []string{
		"period: 2026-11-25..2026-12-01 (7 days)",
		"minutes recorded: 105 total",
		"- work: 90 min",
		"- home: 15 min",
		"total: 105 min",
		"plans: 2 done (completed in period), 1 dropped (dated in period), 1 moved at least once (dated in period), 2 still open (dated in period)",
		"days with something logged: 4",
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("summary missing %q:\n%s", want, res.Text)
		}
	}
	if len(res.Undo) != 0 {
		t.Fatalf("get_summary must be read-only, got undo %+v", res.Undo)
	}

	empty := runTool(t, env, "get_summary", `{"from":"2026-10-01","to":"2026-10-07"}`)
	for _, want := range []string{
		"minutes recorded: 0 total",
		"by project: no minutes recorded",
		"plans: 0 done (completed in period), 0 dropped (dated in period), 0 moved at least once (dated in period), 0 still open (dated in period)",
		"days with something logged: 0",
	} {
		if !strings.Contains(empty.Text, want) {
			t.Fatalf("empty summary missing %q:\n%s", want, empty.Text)
		}
	}
}

func TestGetSummaryValidation(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "get_summary")
	for _, args := range []string{
		`{"from":"2026-12-02","to":"2026-12-01"}`,
		`{"from":"2024-01-01","to":"2026-12-01"}`,
		`{"from":"someday","to":"2026-12-01"}`,
		`{"from":"2026-12-01","to":"nope"}`,
		`{"from":"2026-01-01","to":"2027-01-02"}`,
	} {
		if _, err := toolsRun(env, "get_summary", args); err == nil {
			t.Fatalf("expected error for %s", args)
		}
	}
	if _, err := toolsRun(env, "get_summary", `{"from":"2026-01-01","to":"2027-01-01"}`); err != nil {
		t.Fatalf("366-day range should be accepted: %v", err)
	}
}

func ptrDay(t time.Time) *time.Time { return &t }

func TestLogAtOneThirtyCountsForPreviousDay(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "log_activity", "get_summary")
	// 01:30 on 2 December is the logical day 1 December (04:00 boundary).
	now := time.Date(2026, 12, 2, 1, 30, 0, 0, makassar)
	env.Now = now
	env.Day = store.Day(now.Add(-4 * time.Hour))
	if got := store.DayString(env.Day); got != "2026-12-01" {
		t.Fatalf("logical day: %s", got)
	}
	res := runTool(t, env, "log_activity", `{"description":"late night work","minutes":20}`)
	if !strings.Contains(res.Text, "2026-12-01") {
		t.Fatalf("late activity stored on the wrong day: %s", res.Text)
	}
	sum := runTool(t, env, "get_summary", `{"from":"2026-12-01","to":"2026-12-01"}`)
	if !strings.Contains(sum.Text, "minutes recorded: 20 total") {
		t.Fatalf("summary missed the late activity:\n%s", sum.Text)
	}
}

func toolsRun(env tools.Env, name, args string) (tools.Result, error) {
	return tools.New().Run(context.Background(), env, name, args)
}
