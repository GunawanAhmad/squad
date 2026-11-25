package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"squad/store"
)

// get_summary is the SQL-backed aggregate for "summarize my week/month". It
// reuses the same logical-day semantics as query_activities: day is the user's
// date from the 04:00 boundary, and from/to are inclusive.

func summaryTool() *Tool {
	return &Tool{
		Name:        "get_summary",
		Description: "Read-only totals for a period: minutes logged per project and in total, plans done (by completion time) and dropped/moved/still open (by planned date), and days with something logged. Use it before summarizing a week or month. Dates accept YYYY-MM-DD, today/yesterday; from defaults 7 days ago and to defaults today. Cite these numbers exactly; never recompute or estimate them.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"from": map[string]any{"type": "string", "description": "YYYY-MM-DD or today/yesterday; default 7 days ago"},
				"to":   map[string]any{"type": "string", "description": "YYYY-MM-DD or today; default today"},
			},
		},
		Run: func(ctx context.Context, env Env, raw string) (Result, error) {
			day, err := recordDay(env)
			if err != nil {
				return Result{}, err
			}
			var args struct {
				From string `json:"from"`
				To   string `json:"to"`
			}
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			from, to := day.AddDate(0, 0, -6), day
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
			if to.Sub(from) > 365*24*time.Hour {
				return Result{}, fmt.Errorf("%w: range is longer than a year", ErrInvalid)
			}

			acts, err := env.DB.Activities(ctx, store.ActivityFilter{From: &from, To: &to})
			if err != nil {
				return Result{}, err
			}
			projects, err := env.DB.Projects(ctx)
			if err != nil {
				return Result{}, err
			}
			names := make(map[int64]string, len(projects))
			for _, p := range projects {
				names[p.ID] = p.Name
			}
			plans, err := env.DB.Tasks(ctx, store.TaskFilter{PlannedFrom: &from, PlannedTo: &to})
			if err != nil {
				return Result{}, err
			}
			// Completion is an instant, not a planned date: count done tasks by
			// completed_at so the number agrees with the seven-day digest.
			fromStart := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, env.Loc).Add(env.DayStartsAt)
			toEnd := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, env.Loc).Add(env.DayStartsAt).AddDate(0, 0, 1)
			doneTasks, err := env.DB.Tasks(ctx, store.TaskFilter{
				Status: []string{"done"}, CompletedFrom: &fromStart, CompletedTo: &toEnd,
			})
			if err != nil {
				return Result{}, err
			}

			total := 0
			days := map[string]bool{}
			for _, a := range acts {
				days[store.DayString(a.Day)] = true
				if a.Minutes != nil {
					total += *a.Minutes
				}
			}
			dropped, moved, open := 0, 0, 0
			for _, t := range plans {
				switch t.Status {
				case "dropped":
					dropped++
				case "open":
					open++
				}
				if t.TimesMoved > 0 {
					moved++
				}
			}
			done := len(doneTasks)

			var b strings.Builder
			fmt.Fprintf(&b, "period: %s..%s (%d days)\n", store.DayString(from), store.DayString(to), int(to.Sub(from).Hours()/24)+1)
			fmt.Fprintf(&b, "minutes recorded: %d total\n", total)
			writeProjectTotals(&b, "by project", minutesByProject(acts, names))
			fmt.Fprintf(&b, "plans: %d done (completed in period), %d dropped (dated in period), %d moved at least once (dated in period), %d still open (dated in period)\n", done, dropped, moved, open)
			fmt.Fprintf(&b, "days with something logged: %d\n", len(days))
			return text(b.String()), nil
		},
	}
}
