package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"characters/store"
)

// skip_checkin lets the user say "no check-in tomorrow morning". The row is
// written ahead of time, so the engine never even claims it.

func skipCheckinTool() *Tool {
	return &Tool{
		Name: "skip_checkin",
		Description: "Skip a check-in the user does not want (\"no check-in tomorrow morning\"). " +
			"day defaults to today and cannot be in the past. kind is morning, evening or all.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"day":  map[string]any{"type": "string", "description": "today, tomorrow or YYYY-MM-DD"},
				"kind": map[string]any{"type": "string", "enum": []string{"morning", "evening", "all"}},
			},
		},
		Run: func(ctx context.Context, env Env, raw string) (Result, error) {
			day, err := recordDay(env)
			if err != nil {
				return Result{}, err
			}
			var args struct {
				Day  string `json:"day"`
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			if strings.TrimSpace(args.Day) != "" {
				if day, err = ParseDay(args.Day, day); err != nil {
					return Result{}, err
				}
			}
			today := recordToday(env)
			if day.Before(today) {
				return Result{}, fmt.Errorf("%w: that day is over", ErrInvalid)
			}
			var kinds []string
			switch strings.ToLower(strings.TrimSpace(args.Kind)) {
			case "", "all":
				kinds = []string{"morning", "evening"}
			case "morning", "evening":
				kinds = []string{strings.ToLower(strings.TrimSpace(args.Kind))}
			default:
				return Result{}, fmt.Errorf("%w: kind must be morning, evening or all", ErrInvalid)
			}

			var skipped []string
			var undo []UndoOp
			for _, kind := range kinds {
				if err := env.DB.SkipCheckin(ctx, day, kind); err != nil {
					return Result{}, err
				}
				skipped = append(skipped, kind)
				undo = append(undo, UndoOp{
					Op: "unskip_checkin", CheckinDay: store.DayString(day), CheckinKind: kind,
				})
			}
			return Result{
				Text: fmt.Sprintf("skipped the %s check-in on %s", strings.Join(skipped, " and "), store.DayString(day)),
				Undo: undo,
			}, nil
		},
	}
}

func recordToday(env Env) time.Time {
	if env.Day.IsZero() {
		return store.Day(env.Now)
	}
	return env.Day
}
