package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"characters/store"
)

// Task tools write the "what I want to do" rows. Every write returns the
// inverse operation so undo (chat or Today panel) can put the row back.
// Ambiguous references list candidates instead of guessing.

// recordDay returns the logical day, and fails loudly when a domain tool runs
// without a store (only possible in tests).
func recordDay(env Env) (time.Time, error) {
	if env.DB == nil {
		return time.Time{}, fmt.Errorf("records are not available")
	}
	if env.Day.IsZero() {
		return store.Day(env.Now), nil
	}
	return env.Day, nil
}

func taskRefParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string", "description": "Task id (#12 or 12) or (part of) its title"},
		},
		"required": []string{"task"},
	}
}

func planTasksTool() *Tool {
	return &Tool{
		Name:        "plan_tasks",
		Description: "Record plans. Call this whenever the user states something they want to do (\"I need to\", \"I have to\", \"plan\", \"add\"), even if today's list already shows something similar. One entry per task, the user's own wording. plannedOn defaults to today; use tomorrow or YYYY-MM-DD when they say so.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"title":     map[string]any{"type": "string"},
							"notes":     map[string]any{"type": "string"},
							"project":   map[string]any{"type": "string"},
							"plannedOn": map[string]any{"type": "string", "description": "YYYY-MM-DD, today or tomorrow"},
						},
						"required": []string{"title"},
					},
				},
			},
			"required": []string{"tasks"},
		},
		Run: func(ctx context.Context, env Env, raw string) (Result, error) {
			day, err := recordDay(env)
			if err != nil {
				return Result{}, err
			}
			var args struct {
				Tasks []struct {
					Title     string `json:"title"`
					Notes     string `json:"notes"`
					Project   string `json:"project"`
					PlannedOn string `json:"plannedOn"`
				} `json:"tasks"`
			}
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			if len(args.Tasks) == 0 {
				return Result{}, fmt.Errorf("%w: tasks is empty", ErrInvalid)
			}
			if len(args.Tasks) > 20 {
				return Result{}, fmt.Errorf("%w: at most 20 tasks per call", ErrInvalid)
			}

			var created []store.Task
			var undo []UndoOp
			for _, in := range args.Tasks {
				var tcID *int64
				if env.ToolCallID != 0 {
					id := env.ToolCallID
					tcID = &id
				}
				t, err := CreateTask(ctx, env.DB, in.Title, in.Notes, in.PlannedOn, in.Project, day, tcID, env.SourceMessageID)
				if err != nil {
					return Result{}, err
				}
				created = append(created, t)
				undo = append(undo, UndoOp{
					Op: "delete_task", TaskID: t.ID,
					ExpectUpdated: t.UpdatedAt.Format(time.RFC3339Nano),
				})
			}

			var lines []string
			for _, t := range created {
				lines = append(lines, TaskLine(t, env.Loc))
			}
			return Result{
				Text: fmt.Sprintf("planned %d task(s):\n%s", len(created), strings.Join(lines, "\n")),
				Undo: undo,
			}, nil
		},
	}
}

func updateTaskTool() *Tool {
	params := taskRefParams()
	props := params["properties"].(map[string]any)
	props["title"] = map[string]any{"type": "string"}
	props["notes"] = map[string]any{"type": "string"}
	props["status"] = map[string]any{"type": "string", "enum": []string{"open", "done", "dropped"}}
	props["plannedOn"] = map[string]any{"type": "string", "description": "YYYY-MM-DD, today, tomorrow, or empty to clear"}
	props["dueOn"] = map[string]any{"type": "string"}
	props["project"] = map[string]any{"type": "string"}

	return &Tool{
		Name:        "update_task",
		Description: "Change an existing task: new title, notes, day, status or project (\"move X to Monday\", \"X is tomorrow\", \"rename X\"). Moving a task to a later day counts as a slip. If a title matches more than one task, you get candidates: ask which one.",
		Parameters:  params,
		Run: func(ctx context.Context, env Env, raw string) (Result, error) {
			day, err := recordDay(env)
			if err != nil {
				return Result{}, err
			}
			var args struct {
				Task string `json:"task"`
				TaskPatch
			}
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			found, candidates, err := ResolveTask(ctx, env.DB, args.Task)
			if err != nil {
				return Result{}, err
			}
			if len(candidates) > 0 {
				return text("ambiguous, more than one task matches. Ask which one:\n" + candidateLines(candidates, env.Loc)), nil
			}
			change, err := ApplyTaskPatch(ctx, env.DB, found.ID, args.TaskPatch, env.Now, day)
			if err != nil {
				return Result{}, err
			}
			line := TaskLine(change.After, env.Loc)
			if change.MovedLater {
				line += " (moved later, now slipped " + fmt.Sprint(change.After.TimesMoved) + "x)"
			}
			return Result{
				Text: "updated: " + line,
				Undo: []UndoOp{{
					Op: "restore_task", TaskID: change.After.ID,
					ExpectUpdated: change.After.UpdatedAt.Format(time.RFC3339Nano),
					PreviousTask:  mustJSON(change.Before),
				}},
			}, nil
		},
	}
}

func completeTaskTool() *Tool {
	params := taskRefParams()
	props := params["properties"].(map[string]any)
	props["status"] = map[string]any{
		"type": "string", "enum": []string{"done", "dropped"},
		"description": "done when it happened, dropped when the user decided not to do it",
	}

	return &Tool{
		Name:        "complete_task",
		Description: "Mark a task done or dropped. \"I finished X\" → done; \"I dropped/stopped X\" → status dropped. Ask when it is unclear which task is meant or whether it was done or dropped.",
		Parameters:  params,
		Run: func(ctx context.Context, env Env, raw string) (Result, error) {
			day, err := recordDay(env)
			if err != nil {
				return Result{}, err
			}
			var args struct {
				Task   string `json:"task"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			status := strings.ToLower(strings.TrimSpace(args.Status))
			if status == "" {
				status = "done"
			}
			if status != "done" && status != "dropped" {
				return Result{}, fmt.Errorf("%w: status must be done or dropped", ErrInvalid)
			}
			found, candidates, err := ResolveTask(ctx, env.DB, args.Task)
			if err != nil {
				return Result{}, err
			}
			if len(candidates) > 0 {
				return text("ambiguous, more than one task matches. Ask which one:\n" + candidateLines(candidates, env.Loc)), nil
			}
			change, err := ApplyTaskPatch(ctx, env.DB, found.ID, TaskPatch{Status: &status}, env.Now, day)
			if err != nil {
				return Result{}, err
			}
			return Result{
				Text: fmt.Sprintf("%s: %s", status, TaskLine(change.After, env.Loc)),
				Undo: []UndoOp{{
					Op: "restore_task", TaskID: change.After.ID,
					ExpectUpdated: change.After.UpdatedAt.Format(time.RFC3339Nano),
					PreviousTask:  mustJSON(change.Before),
				}},
			}, nil
		},
	}
}

func listTasksTool() *Tool {
	return &Tool{
		Name:        "list_tasks",
		Description: "Read the task list. Only for questions about existing plans (\"what is left\", \"what did I plan\") or to find a task's exact title; never to record one. Defaults to open tasks.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status":    map[string]any{"type": "string", "enum": []string{"open", "done", "dropped", "all"}},
				"plannedOn": map[string]any{"type": "string", "description": "YYYY-MM-DD, today, tomorrow, or all (default)"},
				"project":   map[string]any{"type": "string"},
				"limit":     map[string]any{"type": "integer", "description": "default 50"},
			},
		},
		Run: func(ctx context.Context, env Env, raw string) (Result, error) {
			day, err := recordDay(env)
			if err != nil {
				return Result{}, err
			}
			var args struct {
				Status    string `json:"status"`
				PlannedOn string `json:"plannedOn"`
				Project   string `json:"project"`
				Limit     int    `json:"limit"`
			}
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			f := store.TaskFilter{Limit: args.Limit}
			if f.Limit <= 0 || f.Limit > 200 {
				f.Limit = 50
			}
			switch strings.ToLower(strings.TrimSpace(args.Status)) {
			case "", "open":
				f.Status = []string{"open"}
			case "all":
			case "done", "dropped":
				f.Status = []string{strings.ToLower(args.Status)}
			default:
				return Result{}, fmt.Errorf("%w: status must be open, done, dropped or all", ErrInvalid)
			}
			if v := strings.TrimSpace(args.PlannedOn); v != "" && !strings.EqualFold(v, "all") {
				d, err := ParseDay(v, day)
				if err != nil {
					return Result{}, err
				}
				f.PlannedOn = &d
			}
			if strings.TrimSpace(args.Project) != "" {
				projects, err := env.DB.Projects(ctx)
				if err != nil {
					return Result{}, err
				}
				for _, p := range projects {
					if strings.EqualFold(p.Name, args.Project) {
						f.ProjectID = &p.ID
					}
				}
				if f.ProjectID == nil {
					return text("no project named " + args.Project), nil
				}
			}
			tasks, err := env.DB.Tasks(ctx, f)
			if err != nil {
				return Result{}, err
			}
			if len(tasks) == 0 {
				return text("no tasks"), nil
			}
			var lines []string
			for _, t := range tasks {
				lines = append(lines, TaskLine(t, env.Loc))
			}
			return text(fmt.Sprintf("%d task(s):\n%s", len(tasks), strings.Join(lines, "\n"))), nil
		},
	}
}

func candidateLines(tasks []store.Task, loc *time.Location) string {
	var lines []string
	for _, t := range tasks {
		lines = append(lines, TaskLine(t, loc))
	}
	return strings.Join(lines, "\n")
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}
