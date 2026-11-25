package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"squad/store"
	"squad/store/storetest"
	"squad/tools"
)

// write runs a write tool and stores its inverse operation the way the turn
// runner does, so undo has something to work with.
func write(t *testing.T, env tools.Env, name, args string) (tools.Result, int64) {
	t.Helper()
	res := runTool(t, env, name, args)
	undo, err := json.Marshal(res.Undo)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.DB.FinishToolCall(context.Background(), env.ToolCallID, res.Text, undo, "ok"); err != nil {
		t.Fatal(err)
	}
	return res, env.ToolCallID
}

func nextCall(t *testing.T, env tools.Env) tools.Env {
	t.Helper()
	call, err := env.DB.StartToolCall(context.Background(), store.ToolCall{RunID: 1, Name: "next", Args: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	env.ToolCallID = call.ID
	return env
}

func TestUndoOneWrite(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "undo", "plan_tasks", "log_activity")
	ctx := context.Background()

	// plan_tasks records one inverse per task; undoing one leaves the other.
	env = nextCall(t, env)
	res, planCall := write(t, env, "plan_tasks", `{"tasks":[{"title":"one"},{"title":"two"}]}`)
	if len(res.Undo) != 2 {
		t.Fatalf("undo ops: %+v", res.Undo)
	}
	tasks, err := db.Tasks(ctx, store.TaskFilter{Order: "created"})
	if err != nil || len(tasks) != 2 {
		t.Fatalf("tasks: %+v, %v", tasks, err)
	}

	summary, err := tools.ApplyUndo(ctx, db, "assistant", planCall, "task", tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, `removed plan #1 "one"`) {
		t.Fatalf("summary: %q", summary)
	}
	if left, _ := db.Tasks(ctx, store.TaskFilter{}); len(left) != 1 || left[0].Title != "two" {
		t.Fatalf("after single undo: %+v", left)
	}
	tc, _ := db.GetToolCall(ctx, planCall)
	if tc.Status != "ok" {
		t.Fatalf("call status after partial undo: %s", tc.Status)
	}

	// Undoing the whole call removes the rest and closes the call.
	summary, err = tools.ApplyUndo(ctx, db, "assistant", planCall, "", 0)
	if err != nil || !strings.Contains(summary, `"two"`) {
		t.Fatalf("whole undo: %q, %v", summary, err)
	}
	if left, _ := db.Tasks(ctx, store.TaskFilter{}); len(left) != 0 {
		t.Fatalf("after whole undo: %+v", left)
	}
	if tc, _ := db.GetToolCall(ctx, planCall); tc.Status != "undone" {
		t.Fatalf("call status: %s", tc.Status)
	}
	if _, err := tools.ApplyUndo(ctx, db, "assistant", planCall, "", 0); !errors.Is(err, tools.ErrConflict) {
		t.Fatalf("second undo: %v", err)
	}

	// The tool path (what the chat uses) produces the same effect and text.
	env = nextCall(t, env)
	_, actCall := write(t, env, "log_activity", `{"description":"gym","minutes":30}`)
	env = nextCall(t, env)
	out := runTool(t, env, "undo", fmt.Sprintf(`{"tool_call_id":%d}`, actCall))
	if !strings.Contains(out.Text, "undone: removed log entry") {
		t.Fatalf("undo tool: %q", out.Text)
	}
	if _, err := tools.New().Run(ctx, env, "undo", `{"tool_call_id":0}`); !errors.Is(err, tools.ErrInvalid) {
		t.Fatalf("missing id: %v", err)
	}
}

func TestUndoRefusesWhenTheRecordChanged(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "plan_tasks", "complete_task", "log_activity")
	ctx := context.Background()

	env = nextCall(t, env)
	_, planCall := write(t, env, "plan_tasks", `{"tasks":[{"title":"finish the report"}]}`)

	// The task moves on (completed) after it was planned.
	env = nextCall(t, env)
	write(t, env, "complete_task", `{"task":"finish the report"}`)

	_, err := tools.ApplyUndo(ctx, db, "assistant", planCall, "", 0)
	if !errors.Is(err, tools.ErrConflict) || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("undo after change: %v", err)
	}
	if left, _ := db.Tasks(ctx, store.TaskFilter{}); len(left) != 1 || left[0].Status != "done" {
		t.Fatalf("task was clobbered: %+v", left)
	}

	// A record that is already gone is not resurrected silently either.
	env = nextCall(t, env)
	_, actCall := write(t, env, "log_activity", `{"description":"temp"}`)
	acts, _ := db.Activities(ctx, store.ActivityFilter{})
	if err := db.DeleteActivity(ctx, acts[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.ApplyUndo(ctx, db, "assistant", actCall, "", 0); !errors.Is(err, tools.ErrConflict) {
		t.Fatalf("undo of a deleted row: %v", err)
	}
}

func TestUndoRestoresTaskAndReminder(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "plan_tasks", "update_task", "remind_me", "cancel_reminder")
	ctx := context.Background()

	env = nextCall(t, env)
	write(t, env, "plan_tasks", `{"tasks":[{"title":"write tests"}]}`)
	env = nextCall(t, env)
	_, updateCall := write(t, env, "update_task", `{"task":"write tests","plannedOn":"tomorrow"}`)
	task, _ := db.GetTask(ctx, 1)
	if task.TimesMoved != 1 {
		t.Fatalf("times moved: %+v", task)
	}
	if _, err := tools.ApplyUndo(ctx, db, "assistant", updateCall, "", 0); err != nil {
		t.Fatal(err)
	}
	back, _ := db.GetTask(ctx, 1)
	if back.TimesMoved != 0 || !store.Day(*back.PlannedOn).Equal(toolsDay()) {
		t.Fatalf("restored task: %+v", back)
	}

	env = nextCall(t, env)
	write(t, env, "remind_me", `{"text":"call mom","at":"15:00"}`)
	env = nextCall(t, env)
	_, cancelCall := write(t, env, "cancel_reminder", `{"reminder":"call mom"}`)
	if r, _ := db.GetReminder(ctx, 1); r.Status != "cancelled" {
		t.Fatalf("cancelled: %+v", r)
	}
	if _, err := tools.ApplyUndo(ctx, db, "assistant", cancelCall, "reminder", 1); err != nil {
		t.Fatal(err)
	}
	if r, _ := db.GetReminder(ctx, 1); r.Status != "pending" {
		t.Fatalf("restored reminder: %+v", r)
	}
	// Undoing the same cancel twice is refused.
	if _, err := tools.ApplyUndo(ctx, db, "assistant", cancelCall, "", 0); !errors.Is(err, tools.ErrConflict) {
		t.Fatalf("second undo: %v", err)
	}
}
