package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"squad/store"
)

// Reminder tools write the "tell me at ..." rows. Times are parsed in the
// user's zone (settings.tz); a time in the past is rejected, not fired.

func remindMeTool() *Tool {
	return &Tool{
		Name:        "remind_me",
		Description: "Set a reminder. Only when the user asks to be reminded. at is a local time: \"15:00\", \"tomorrow 09:00\", \"in 30m\" or a full date. repeat is daily or weekdays for a standing reminder. Never schedule a time in the past.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text":   map[string]any{"type": "string", "description": "what to remind the user about, in their words"},
				"at":     map[string]any{"type": "string", "description": "15:00, tomorrow 09:00, in 30m, or 2026-12-01 15:00"},
				"repeat": map[string]any{"type": "string", "enum": []string{"", "daily", "weekdays"}},
			},
			"required": []string{"text", "at"},
		},
		Run: func(ctx context.Context, env Env, raw string) (Result, error) {
			if _, err := recordDay(env); err != nil {
				return Result{}, err
			}
			var args struct {
				Text   string `json:"text"`
				At     string `json:"at"`
				Repeat string `json:"repeat"`
			}
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			var tcID *int64
			if env.ToolCallID != 0 {
				id := env.ToolCallID
				tcID = &id
			}
			rem, err := AddReminder(ctx, env.DB, args.Text, args.At, args.Repeat, env.Now, env.Loc, tcID, env.SourceMessageID)
			if err != nil {
				return Result{}, err
			}
			return Result{
				Text: "reminder set: " + ReminderLine(rem, env.Loc),
				Undo: []UndoOp{{Op: "cancel_reminder", ReminderID: rem.ID}},
			}, nil
		},
	}
}

func listRemindersTool() *Tool {
	return &Tool{
		Name:        "list_reminders",
		Description: "List reminders. Defaults to pending ones, soonest first.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{"type": "string", "enum": []string{"pending", "fired", "cancelled", "all"}},
				"limit":  map[string]any{"type": "integer", "description": "default 50"},
			},
		},
		Run: func(ctx context.Context, env Env, raw string) (Result, error) {
			if _, err := recordDay(env); err != nil {
				return Result{}, err
			}
			var args struct {
				Status string `json:"status"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			f := store.ReminderFilter{Limit: args.Limit}
			if f.Limit <= 0 || f.Limit > 200 {
				f.Limit = 50
			}
			switch strings.ToLower(strings.TrimSpace(args.Status)) {
			case "", "pending":
				f.Status = []string{"pending"}
			case "all":
			case "fired", "cancelled":
				f.Status = []string{strings.ToLower(args.Status)}
			default:
				return Result{}, fmt.Errorf("%w: status must be pending, fired, cancelled or all", ErrInvalid)
			}
			reminders, err := env.DB.Reminders(ctx, f)
			if err != nil {
				return Result{}, err
			}
			if len(reminders) == 0 {
				return text("no reminders"), nil
			}
			var lines []string
			for _, r := range reminders {
				lines = append(lines, ReminderLine(r, env.Loc))
			}
			return text(fmt.Sprintf("%d reminder(s):\n%s", len(reminders), strings.Join(lines, "\n"))), nil
		},
	}
}

func cancelReminderTool() *Tool {
	return &Tool{
		Name:        "cancel_reminder",
		Description: "Cancel a pending reminder by its #id or its text.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reminder": map[string]any{"type": "string", "description": "#7, 7, or (part of) the reminder text"},
			},
			"required": []string{"reminder"},
		},
		Run: func(ctx context.Context, env Env, raw string) (Result, error) {
			if _, err := recordDay(env); err != nil {
				return Result{}, err
			}
			var args struct {
				Reminder string `json:"reminder"`
			}
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			found, candidates, err := resolveReminder(ctx, env, args.Reminder)
			if err != nil {
				return Result{}, err
			}
			if len(candidates) > 0 {
				var lines []string
				for _, r := range candidates {
					lines = append(lines, ReminderLine(r, env.Loc))
				}
				return text("ambiguous, more than one reminder matches. Ask which one:\n" + strings.Join(lines, "\n")), nil
			}
			before := found
			found.Status = "cancelled"
			updated, err := env.DB.UpdateReminder(ctx, found)
			if err != nil {
				return Result{}, err
			}
			return Result{
				Text: "cancelled: " + ReminderLine(updated, env.Loc),
				Undo: []UndoOp{{
					Op: "restore_reminder", ReminderID: before.ID,
					ExpectStatus: updated.Status, PreviousRemind: mustJSON(before),
				}},
			}, nil
		},
	}
}

func resolveReminder(ctx context.Context, env Env, ref string) (store.Reminder, []store.Reminder, error) {
	ref = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), "#"))
	if ref == "" {
		return store.Reminder{}, nil, fmt.Errorf("%w: which reminder?", ErrInvalid)
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		r, err := env.DB.GetReminder(ctx, id)
		return r, nil, err
	}
	all, err := env.DB.Reminders(ctx, store.ReminderFilter{Status: []string{"pending"}})
	if err != nil {
		return store.Reminder{}, nil, err
	}
	lower := strings.ToLower(ref)
	var exact, partial []store.Reminder
	for _, r := range all {
		switch {
		case strings.EqualFold(r.Text, lower):
			exact = append(exact, r)
		case strings.Contains(strings.ToLower(r.Text), lower):
			partial = append(partial, r)
		}
	}
	for _, set := range [][]store.Reminder{exact, partial} {
		if len(set) == 1 {
			return set[0], nil, nil
		}
		if len(set) > 1 {
			return store.Reminder{}, set, nil
		}
	}
	return store.Reminder{}, nil, fmt.Errorf("%w: no pending reminder matches %q", store.ErrNotFound, ref)
}
