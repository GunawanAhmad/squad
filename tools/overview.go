package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"characters/store"
)

// The overview is the compact, read-only picture the model gets before it is
// allowed to suggest what to focus on. Every number here comes from the store;
// the persona is not allowed to recompute or estimate them.

const (
	overviewPlanCap    = 15
	overviewProjectCap = 10
	overviewGoalCap    = 10
)

type projectTotal struct {
	Name    string
	Minutes int
}

func overviewTool() *Tool {
	return &Tool{
		Name:        "get_overview",
		Description: "Read-only overview before suggesting what to focus on: open plans with their age and times moved, minutes per project for the last 7 and 30 days, and active goal facts. Call this before giving focus advice; cite the plan or fact behind each suggestion and never invent numbers.",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Run: func(ctx context.Context, env Env, _ string) (Result, error) {
			day, err := recordDay(env)
			if err != nil {
				return Result{}, err
			}
			plans, err := env.DB.Tasks(ctx, store.TaskFilter{Status: []string{"open"}, Order: "planned"})
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
			from7 := day.AddDate(0, 0, -6)
			from30 := day.AddDate(0, 0, -29)
			acts7, err := env.DB.Activities(ctx, store.ActivityFilter{From: &from7, To: &day})
			if err != nil {
				return Result{}, err
			}
			acts30, err := env.DB.Activities(ctx, store.ActivityFilter{From: &from30, To: &day})
			if err != nil {
				return Result{}, err
			}
			goals, err := env.DB.Facts(ctx, env.CharacterID, store.FactFilter{
				Status: []string{"active"}, Category: "goal", Order: "recent",
			})
			if err != nil {
				return Result{}, err
			}

			var b strings.Builder
			fmt.Fprintf(&b, "today: %s\n", store.DayString(day))
			writePlans(&b, plans, day)
			writeProjectTotals(&b, "minutes per project, last 7 days ("+store.DayString(from7)+".."+store.DayString(day)+")", minutesByProject(acts7, names))
			writeProjectTotals(&b, "minutes per project, last 30 days ("+store.DayString(from30)+".."+store.DayString(day)+")", minutesByProject(acts30, names))
			writeGoals(&b, goals)
			return text(b.String()), nil
		},
	}
}

func writePlans(b *strings.Builder, plans []store.Task, day time.Time) {
	if len(plans) == 0 {
		b.WriteString("open plans: none\n")
		return
	}
	fmt.Fprintf(b, "open plans (%d total):\n", len(plans))
	shown := 0
	for _, t := range plans {
		if shown == overviewPlanCap {
			break
		}
		if t.PlannedOn == nil {
			fmt.Fprintf(b, "- #%d %s — no planned date, moved %dx\n", t.ID, t.Title, t.TimesMoved)
			shown++
			continue
		}
		planned := store.DayString(*t.PlannedOn)
		age := daysBetween(*t.PlannedOn, day)
		if age < 0 {
			fmt.Fprintf(b, "- #%d %s — planned %s, moved %dx\n", t.ID, t.Title, planned, t.TimesMoved)
		} else {
			fmt.Fprintf(b, "- #%d %s — planned %s, age %dd, moved %dx\n", t.ID, t.Title, planned, age, t.TimesMoved)
		}
		shown++
	}
	if len(plans) > shown {
		fmt.Fprintf(b, "+%d more open plans — call list_tasks for the rest\n", len(plans)-shown)
	}
}

func writeProjectTotals(b *strings.Builder, label string, totals []projectTotal) {
	if len(totals) == 0 {
		fmt.Fprintf(b, "%s: no minutes recorded\n", label)
		return
	}
	fmt.Fprintf(b, "%s:\n", label)
	shown := 0
	total := 0
	for _, t := range totals {
		total += t.Minutes
		if shown == overviewProjectCap {
			continue
		}
		fmt.Fprintf(b, "- %s: %d min\n", t.Name, t.Minutes)
		shown++
	}
	fmt.Fprintf(b, "total: %d min\n", total)
	if len(totals) > shown {
		fmt.Fprintf(b, "+%d more projects\n", len(totals)-shown)
	}
}

func writeGoals(b *strings.Builder, goals []store.Fact) {
	if len(goals) == 0 {
		b.WriteString("goal facts: none\n")
		return
	}
	fmt.Fprintf(b, "goal facts (%d total):\n", len(goals))
	shown := 0
	for _, f := range goals {
		if shown == overviewGoalCap {
			break
		}
		fmt.Fprintf(b, "- #%d %s\n", f.ID, f.Text)
		shown++
	}
	if len(goals) > shown {
		fmt.Fprintf(b, "+%d more goal facts\n", len(goals)-shown)
	}
}

// daysBetween counts calendar days from earlier to later; a future plan has a
// negative age (the caller prints it without one).
func daysBetween(earlier, later time.Time) int {
	return int(store.Day(later).Sub(store.Day(earlier)).Hours() / 24)
}

func minutesByProject(acts []store.Activity, names map[int64]string) []projectTotal {
	byName := map[string]int{}
	for _, a := range acts {
		if a.Minutes == nil {
			continue
		}
		name := "(no project)"
		if a.ProjectID != nil {
			if n, ok := names[*a.ProjectID]; ok {
				name = n
			}
		}
		byName[name] += *a.Minutes
	}
	out := make([]projectTotal, 0, len(byName))
	for name, minutes := range byName {
		out = append(out, projectTotal{Name: name, Minutes: minutes})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Minutes != out[j].Minutes {
			return out[i].Minutes > out[j].Minutes
		}
		return out[i].Name < out[j].Name
	})
	return out
}
