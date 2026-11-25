package tools_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"characters/store"
	"characters/store/storetest"
)

func TestGetOverview(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "get_overview")
	ctx := context.Background()

	work, err := db.EnsureProject(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	home, err := db.EnsureProject(ctx, "home")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		planned := env.Day.AddDate(0, 0, -i-1)
		if _, err := db.CreateTask(ctx, store.Task{
			Title: fmt.Sprintf("plan %02d", i), Status: "open", PlannedOn: &planned, TimesMoved: i % 3,
		}); err != nil {
			t.Fatal(err)
		}
	}
	done := env.Day.AddDate(0, 0, -1)
	if _, err := db.CreateTask(ctx, store.Task{Title: "finished thing", Status: "done", PlannedOn: &done}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateTask(ctx, store.Task{Title: "dropped thing", Status: "dropped", PlannedOn: &done}); err != nil {
		t.Fatal(err)
	}

	add := func(desc string, minutes int, dayOffset int, project *int64) {
		t.Helper()
		m := minutes
		if _, err := db.AddActivity(ctx, store.Activity{
			Description: desc, Minutes: &m, Day: env.Day.AddDate(0, 0, dayOffset), ProjectID: project,
		}); err != nil {
			t.Fatal(err)
		}
	}
	add("recent work", 120, -1, &work.ID)
	add("month home", 60, -10, &home.ID)
	add("month work", 30, -20, &work.ID)
	add("too old", 999, -40, &work.ID)
	add("no project", 15, -1, nil)

	for i := 0; i < 12; i++ {
		if _, err := db.AddFact(ctx, store.Fact{
			Text: fmt.Sprintf("goal %02d", i), Category: "goal", ValidFrom: env.Day.AddDate(0, 0, -30),
		}); err != nil {
			t.Fatal(err)
		}
	}
	old, err := db.AddFact(ctx, store.Fact{Text: "old goal", Category: "goal", ValidFrom: env.Day.AddDate(0, 0, -40)})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := db.AddFact(ctx, store.Fact{Text: "new goal", Category: "goal", ValidFrom: env.Day})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SupersedeOn(ctx, old.ID, newer.ID, env.Day); err != nil {
		t.Fatal(err)
	}
	gone, err := db.AddFact(ctx, store.Fact{Text: "forgotten goal", Category: "goal", ValidFrom: env.Day})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ForgetFact(ctx, gone.ID, env.Day); err != nil {
		t.Fatal(err)
	}

	res := runTool(t, env, "get_overview", `{}`)
	for _, want := range []string{
		"open plans (20 total)",
		"+5 more open plans",
		"call list_tasks",
		"minutes per project, last 7 days",
		"- work: 120 min",
		"- (no project): 15 min",
		"total: 135 min",
		"minutes per project, last 30 days",
		"- work: 150 min",
		"- home: 60 min",
		"total: 225 min",
		"goal facts (13 total)",
		"+3 more goal facts",
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("overview missing %q:\n%s", want, res.Text)
		}
	}
	for _, bad := range []string{"finished thing", "dropped thing", "old goal", "forgotten goal"} {
		if strings.Contains(res.Text, bad) {
			t.Fatalf("overview included %q:\n%s", bad, res.Text)
		}
	}
	if len(res.Undo) != 0 {
		t.Fatalf("get_overview must be read-only, got undo %+v", res.Undo)
	}
	if n := strings.Count(res.Text, "- #"); n != 15+10 {
		t.Fatalf("expected 15 plans and 10 goals, got %d bullet lines:\n%s", n, res.Text)
	}
}
