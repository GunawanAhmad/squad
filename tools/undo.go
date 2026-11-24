package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// undo reverses one recorded write, using the inverse operation stored with
// that tool call. The chat calls it as a tool (the write's result prints the
// tool_call_id); the Today panel calls the same ApplyUndo through the REST API.

func undoTool() *Tool {
	return &Tool{
		Name: "undo",
		Description: "Undo one recorded write. Every write result ends with \"(undo: tool_call_id=N)\"; pass that N. " +
			"Refuses when the record changed since (for example a task that is done now), and says so.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool_call_id": map[string]any{"type": "integer", "description": "the id from the write's result"},
			},
			"required": []string{"tool_call_id"},
		},
		Run: func(ctx context.Context, env Env, raw string) (Result, error) {
			if _, err := recordDay(env); err != nil {
				return Result{}, err
			}
			var args struct {
				ToolCallID int64 `json:"tool_call_id"`
			}
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			if args.ToolCallID <= 0 {
				return Result{}, fmt.Errorf("%w: tool_call_id is required", ErrInvalid)
			}
			summary, err := ApplyUndo(ctx, env.DB, args.ToolCallID, "", 0)
			if err != nil {
				return Result{}, err
			}
			return text("undone: " + summary), nil
		},
	}
}
