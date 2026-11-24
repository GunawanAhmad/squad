package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"characters/store"
)

// Activity tools write and read "what I did". Minutes are only stored when the
// user said them; the assistant never guesses a duration.

func logActivityTool() *Tool {
	return &Tool{
		Name:        "log_activity",
		Description: "Record something the user did (past or just finished). Never use it for future plans. minutes only when they said how long. day defaults to today (yesterday/YYYY-MM-DD otherwise). Link a task when they finished one.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description": map[string]any{"type": "string"},
				"minutes":     map[string]any{"type": "integer", "description": "1-1440, only if the user said it"},
				"day":         map[string]any{"type": "string", "description": "YYYY-MM-DD, today or yesterday"},
				"project":     map[string]any{"type": "string"},
				"task":        map[string]any{"type": "string", "description": "optional task id or title"},
			},
			"required": []string{"description"},
		},
		Run: func(ctx context.Context, env Env, raw string) (Result, error) {
			day, err := recordDay(env)
			if err != nil {
				return Result{}, err
			}
			var args struct {
				Description string `json:"description"`
				Minutes     *int   `json:"minutes"`
				Day         string `json:"day"`
				Project     string `json:"project"`
				Task        string `json:"task"`
			}
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			var tcID *int64
			if env.ToolCallID != 0 {
				id := env.ToolCallID
				tcID = &id
			}
			a, err := AddActivity(ctx, env.DB, args.Description, args.Minutes, args.Day, args.Project, day, tcID, env.SourceMessageID)
			if err != nil {
				return Result{}, err
			}
			linked := ""
			if strings.TrimSpace(args.Task) != "" {
				task, candidates, err := ResolveTask(ctx, env.DB, args.Task)
				switch {
				case err != nil:
					// Logged anyway; the link is secondary.
					linked = " (task not linked: " + err.Error() + ")"
				case len(candidates) > 0:
					linked = " (task not linked, ambiguous: " + candidateLines(candidates, env.Loc) + ")"
				default:
					a.TaskID = &task.ID
					if _, err := env.DB.UpdateActivity(ctx, a); err != nil {
						return Result{}, err
					}
					linked = " (linked to " + TaskLine(task, env.Loc) + ")"
				}
			}
			return Result{
				Text: fmt.Sprintf("logged on %s: %s%s", store.DayString(a.Day), ActivityLine(a), linked),
				Undo: []UndoOp{{Op: "delete_activity", ActivityID: a.ID}},
			}, nil
		},
	}
}

func queryActivitiesTool() *Tool {
	return &Tool{
		Name:        "query_activities",
		Description: "Read back what was recorded for a day or a range, with totals. Only for questions about what the user did. Use it before answering \"what did I do today/yesterday\": the answer must come from these rows, never from memory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"from":    map[string]any{"type": "string", "description": "YYYY-MM-DD, today, yesterday (default today)"},
				"to":      map[string]any{"type": "string", "description": "YYYY-MM-DD (default same as from)"},
				"project": map[string]any{"type": "string"},
			},
		},
		Run: func(ctx context.Context, env Env, raw string) (Result, error) {
			day, err := recordDay(env)
			if err != nil {
				return Result{}, err
			}
			var args struct {
				From    string `json:"from"`
				To      string `json:"to"`
				Project string `json:"project"`
			}
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			from, to := day, day
			if strings.TrimSpace(args.From) != "" {
				if from, err = ParseDay(args.From, day); err != nil {
					return Result{}, err
				}
			}
			if strings.TrimSpace(args.To) != "" {
				if to, err = ParseDay(args.To, day); err != nil {
					return Result{}, err
				}
			}
			if to.Before(from) {
				return Result{}, fmt.Errorf("%w: to is before from", ErrInvalid)
			}
			if to.Sub(from) > 366*24*time.Hour {
				return Result{}, fmt.Errorf("%w: range is longer than a year", ErrInvalid)
			}
			f := store.ActivityFilter{From: &from, To: &to}
			if strings.TrimSpace(args.Project) != "" {
				projects, err := env.DB.Projects(ctx)
				if err != nil {
					return Result{}, err
				}
				found := false
				for _, p := range projects {
					if strings.EqualFold(p.Name, args.Project) {
						f.ProjectID, found = &p.ID, true
					}
				}
				if !found {
					return text("no project named " + args.Project), nil
				}
			}
			acts, err := env.DB.Activities(ctx, f)
			if err != nil {
				return Result{}, err
			}
			return text(activitiesSummary(acts, from, to)), nil
		},
	}
}

// activitiesSummary groups rows by day and adds up the minutes that were
// actually recorded, saying how many entries have no minutes.
func activitiesSummary(acts []store.Activity, from, to time.Time) string {
	if len(acts) == 0 {
		return fmt.Sprintf("no activities recorded from %s to %s", from.Format("2006-01-02"), to.Format("2006-01-02"))
	}
	var b strings.Builder
	total, unknown := 0, 0
	var current string
	for _, a := range acts {
		day := store.DayString(a.Day)
		if day != current {
			if current != "" {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "%s:\n", day)
			current = day
		}
		fmt.Fprintf(&b, "- %s\n", ActivityLine(a))
		if a.Minutes != nil {
			total += *a.Minutes
		} else {
			unknown++
		}
	}
	fmt.Fprintf(&b, "total: %d entries, %d minutes recorded", len(acts), total)
	if unknown > 0 {
		fmt.Fprintf(&b, " (%d without minutes)", unknown)
	}
	return b.String()
}
