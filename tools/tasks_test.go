package tools_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"squad/store"
	"squad/store/storetest"
	"squad/tools"
)

var (
	now2026  = time.Date(2026, 12, 1, 1, 0, 0, 0, time.UTC) // 09:00 Asia/Makassar
	makassar = mustLoc("Asia/Makassar")
)

func mustLoc(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// envFor seeds a conversation, run and tool call, because records reference the
// write that created them.
func envFor(t *testing.T, db *store.Store, allowed ...string) (tools.Env, int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.CreateConversation(ctx, store.Conversation{ID: "daily:2026-12-01", CharacterID: "assistant"}); err != nil {
		t.Fatal(err)
	}
	conv := "daily:2026-12-01"
	runID, err := db.StartRun(ctx, store.Run{Kind: "turn", ConversationID: &conv, Model: "fake", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	call, err := db.StartToolCall(ctx, store.ToolCall{RunID: runID, Name: "test", Args: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := db.AddMessage(ctx, store.Message{ConversationID: conv, Role: "user", Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	return tools.Env{
		CharacterID: "assistant", ConversationID: conv, Allowed: allowed,
		Now: now2026, DB: db, Loc: makassar,
		Day:             toolsDay(),
		DayStartsAt:     4 * time.Hour,
		ToolCallID:      call.ID,
		SourceMessageID: &msg.ID,
	}, call.ID
}

func toolsDay() time.Time {
	return time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
}

func runTool(t *testing.T, env tools.Env, name, args string) tools.Result {
	t.Helper()
	res, err := tools.New().Run(context.Background(), env, name, args)
	if err != nil {
		t.Fatalf("%s(%s): %v", name, args, err)
	}
	return res
}

func TestPlanAndListTasks(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "plan_tasks", "list_tasks")

	res := runTool(t, env, "plan_tasks", `{"tasks":[
		{"title":"write the tests","project":"assistant"},
		{"title":"go to the gym","plannedOn":"tomorrow"}
	]}`)
	if !strings.Contains(res.Text, "planned 2 task(s)") || !strings.Contains(res.Text, "write the tests") {
		t.Fatalf("plan text: %q", res.Text)
	}
	if len(res.Undo) != 2 || res.Undo[0].Op != "delete_task" {
		t.Fatalf("undo payload: %+v", res.Undo)
	}

	tasks, err := db.Tasks(context.Background(), store.TaskFilter{Order: "created"})
	if err != nil || len(tasks) != 2 {
		t.Fatalf("tasks: %+v, %v", tasks, err)
	}
	if !store.Day(*tasks[0].PlannedOn).Equal(toolsDay()) {
		t.Fatalf("default planned_on: %v", tasks[0].PlannedOn)
	}
	if !store.Day(*tasks[1].PlannedOn).Equal(toolsDay().AddDate(0, 0, 1)) {
		t.Fatalf("tomorrow: %v", tasks[1].PlannedOn)
	}
	if tasks[0].ProjectID == nil || tasks[0].ToolCallID == nil || tasks[0].SourceMessageID == nil {
		t.Fatalf("provenance not stamped: %+v", tasks[0])
	}

	listed := runTool(t, env, "list_tasks", `{}`)
	if !strings.Contains(listed.Text, "2 task(s)") || !strings.Contains(listed.Text, "#") {
		t.Fatalf("list: %q", listed.Text)
	}
	empty := runTool(t, env, "list_tasks", `{"plannedOn":"yesterday"}`)
	if empty.Text != "no tasks" {
		t.Fatalf("empty list: %q", empty.Text)
	}
}

func TestUpdateAndCompleteTask(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "plan_tasks", "update_task", "complete_task")

	runTool(t, env, "plan_tasks", `{"tasks":[{"title":"finish the report"},{"title":"report the bug"}]}`)

	// Two tasks contain "report": the tool returns candidates instead of guessing.
	ambiguous := runTool(t, env, "update_task", `{"task":"report","plannedOn":"tomorrow"}`)
	if !strings.Contains(ambiguous.Text, "ambiguous") || !strings.Contains(ambiguous.Text, "finish the report") {
		t.Fatalf("ambiguity: %q", ambiguous.Text)
	}

	// An exact title is not ambiguous even though another title contains it.
	updated := runTool(t, env, "update_task", `{"task":"finish the report","plannedOn":"tomorrow"}`)
	if !strings.Contains(updated.Text, "2026-12-02") || !strings.Contains(updated.Text, "slipped 1x") {
		t.Fatalf("update: %q", updated.Text)
	}
	if len(updated.Undo) != 1 || updated.Undo[0].Op != "restore_task" {
		t.Fatalf("undo: %+v", updated.Undo)
	}

	// Moving to the same day does not count as a slip.
	same := runTool(t, env, "update_task", `{"task":"#1","plannedOn":"tomorrow"}`)
	if strings.Contains(same.Text, "slipped") {
		t.Fatalf("same-day move counted as slip: %q", same.Text)
	}

	done := runTool(t, env, "complete_task", `{"task":"finish the report"}`)
	if !strings.Contains(done.Text, "done") || !strings.Contains(done.Text, "finish the report") {
		t.Fatalf("complete: %q", done.Text)
	}
	task, err := db.GetTask(context.Background(), 1)
	if err != nil || task.Status != "done" || task.CompletedAt == nil {
		t.Fatalf("completed task: %+v, %v", task, err)
	}

	dropped := runTool(t, env, "complete_task", `{"task":"report the bug","status":"dropped"}`)
	if !strings.Contains(dropped.Text, "dropped") {
		t.Fatalf("drop: %q", dropped.Text)
	}
}

func TestTaskToolValidation(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "plan_tasks", "update_task", "list_tasks", "complete_task")
	ctx := context.Background()

	runTool(t, env, "plan_tasks", `{"tasks":[{"title":"a real task"}]}`)

	cases := []struct {
		name, tool, args string
		want             error
	}{
		{"empty title", "plan_tasks", `{"tasks":[{"title":"  "}]}`, tools.ErrInvalid},
		{"bad date", "plan_tasks", `{"tasks":[{"title":"x","plannedOn":"next tuesday"}]}`, tools.ErrInvalid},
		{"no tasks", "plan_tasks", `{"tasks":[]}`, tools.ErrInvalid},
		{"bad status", "list_tasks", `{"status":"later"}`, tools.ErrInvalid},
		{"unknown task", "complete_task", `{"task":"nothing like this"}`, store.ErrNotFound},
		{"empty ref", "complete_task", `{"task":""}`, tools.ErrInvalid},
		{"bad patch status", "update_task", `{"task":"1","status":"later"}`, tools.ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := tools.New().Run(ctx, env, c.tool, c.args)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}

	// A tool outside the allowlist never runs.
	if _, err := tools.New().Run(ctx, env, "web_search", `{"query":"x"}`); err == nil {
		t.Fatal("allowlist not enforced")
	}
}

func TestTaskDaysFollowTheLogicalDay(t *testing.T) {
	// 03:30 local still belongs to the previous day: plannedOn "today" is the
	// date the user woke up on, not the date on the wall clock.
	db := storetest.New(t)
	env, _ := envFor(t, db, "plan_tasks")
	late := time.Date(2026, 11, 30, 19, 30, 0, 0, time.UTC) // 03:30 on 1 Dec in Makassar
	env.Now, env.Day = late, time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)

	runTool(t, env, "plan_tasks", `{"tasks":[{"title":"late night"}]}`)
	tasks, err := db.Tasks(context.Background(), store.TaskFilter{})
	if err != nil || len(tasks) != 1 {
		t.Fatal(err)
	}
	if !store.Day(*tasks[0].PlannedOn).Equal(time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("planned_on = %v, want 2026-11-30", tasks[0].PlannedOn)
	}
}

func TestParseDayWeekdays(t *testing.T) {
	base := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC) // Tuesday
	cases := map[string]time.Time{
		"today":      time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
		"tomorrow":   time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC),
		"yesterday":  time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC),
		"Monday":     time.Date(2026, 12, 7, 0, 0, 0, 0, time.UTC), // next Monday, not today
		"2026-12-05": time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC),
	}
	for in, want := range cases {
		got, err := tools.ParseDay(in, base)
		if err != nil || !got.Equal(want) {
			t.Fatalf("ParseDay(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := tools.ParseDay("someday", base); !errors.Is(err, tools.ErrInvalid) {
		t.Fatalf("bad day: %v", err)
	}
}

func TestParseDayIndonesian(t *testing.T) {
	base := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"hari ini": base,
		"besok":    base.AddDate(0, 0, 1),
		"kemarin":  base.AddDate(0, 0, -1),
		"lusa":     base.AddDate(0, 0, 2),
	}
	for in, want := range cases {
		got, err := tools.ParseDay(in, base)
		if err != nil || !got.Equal(want) {
			t.Fatalf("ParseDay(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}
