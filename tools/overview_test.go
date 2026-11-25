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
	done := env.Day.AddDate(0, 0, -30) // oldest, so it would show if the filter were missing
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

	if _, err := db.AddFact(ctx, store.Fact{CharacterID: env.CharacterID,
		Text: "prefers tea", Category: "preference", ValidFrom: env.Day.AddDate(0, 0, -10),
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if _, err := db.AddFact(ctx, store.Fact{CharacterID: env.CharacterID,
			Text: fmt.Sprintf("goal %02d", i), Category: "goal", ValidFrom: env.Day.AddDate(0, 0, -30),
		}); err != nil {
			t.Fatal(err)
		}
	}
	old, err := db.AddFact(ctx, store.Fact{CharacterID: env.CharacterID, Text: "old goal", Category: "goal", ValidFrom: env.Day.AddDate(0, 0, -40)})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := db.AddFact(ctx, store.Fact{CharacterID: env.CharacterID, Text: "new goal", Category: "goal", ValidFrom: env.Day})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SupersedeOn(ctx, env.CharacterID, old.ID, newer.ID, env.Day); err != nil {
		t.Fatal(err)
	}
	gone, err := db.AddFact(ctx, store.Fact{CharacterID: env.CharacterID, Text: "forgotten goal", Category: "goal", ValidFrom: env.Day})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ForgetFact(ctx, env.CharacterID, gone.ID, env.Day); err != nil {
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
		"#20 plan 19 — planned 2026-11-11, age 20d, moved 1x",
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("overview missing %q:\n%s", want, res.Text)
		}
	}
	for _, bad := range []string{"finished thing", "dropped thing", "old goal", "forgotten goal", "prefers tea", "plan 00"} {
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

func TestOverviewProjectCap(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "get_overview")
	ctx := context.Background()
	for i := 1; i <= 12; i++ {
		name := fmt.Sprintf("project %02d", i)
		p, err := db.EnsureProject(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		m := 1
		if _, err := db.AddActivity(ctx, store.Activity{
			Description: "work", Minutes: &m, Day: env.Day, ProjectID: &p.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	res := runTool(t, env, "get_overview", `{}`)
	if !strings.Contains(res.Text, "+2 more projects") {
		t.Fatalf("project cap missing:\n%s", res.Text)
	}
	if n := strings.Count(res.Text, ": 1 min\n"); n != 20 {
		// 10 projects in each of the 7-day and 30-day blocks.
		t.Fatalf("expected 10 projects per period, got %d:\n%s", n, res.Text)
	}
}

func TestOverviewFuturePlanKeepsMoved(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "get_overview")
	planned := env.Day.AddDate(0, 0, 2)
	if _, err := db.CreateTask(context.Background(), store.Task{
		Title: "future plan", Status: "open", PlannedOn: &planned, TimesMoved: 3,
	}); err != nil {
		t.Fatal(err)
	}
	res := runTool(t, env, "get_overview", `{}`)
	if !strings.Contains(res.Text, "planned 2026-12-03, moved 3x") {
		t.Fatalf("future plan lost its moved count:\n%s", res.Text)
	}
	if strings.Contains(res.Text, "age -") {
		t.Fatalf("negative age printed:\n%s", res.Text)
	}
}

func TestOverviewWindows(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "get_overview")
	ctx := context.Background()
	p, err := db.EnsureProject(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	add := func(offset, minutes int) {
		t.Helper()
		m := minutes
		if _, err := db.AddActivity(ctx, store.Activity{
			Description: "work", Minutes: &m, Day: env.Day.AddDate(0, 0, offset), ProjectID: &p.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	add(-6, 10)  // inside 7 and 30
	add(-7, 20)  // outside 7, inside 30
	add(-29, 30) // outside 7, inside 30
	add(-30, 40) // outside both
	res := runTool(t, env, "get_overview", `{}`)
	if !strings.Contains(res.Text, "minutes per project, last 7 days") ||
		!strings.Contains(res.Text, "total: 10 min") ||
		!strings.Contains(res.Text, "total: 60 min") {
		t.Fatalf("window totals wrong:\n%s", res.Text)
	}
}

func TestOverviewEmpty(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "get_overview")
	res := runTool(t, env, "get_overview", `{}`)
	for _, want := range []string{"open plans: none", "goal facts: none", "no minutes recorded"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("empty overview missing %q:\n%s", want, res.Text)
		}
	}
}
