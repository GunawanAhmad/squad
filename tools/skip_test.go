package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"characters/store"
	"characters/store/storetest"
	"characters/tools"
)

func TestSkipCheckin(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "skip_checkin", "undo")
	ctx := context.Background()

	res := runTool(t, env, "skip_checkin", `{"day":"tomorrow","kind":"morning"}`)
	if !strings.Contains(res.Text, "skipped the morning check-in on 2026-12-02") {
		t.Fatalf("skip: %q", res.Text)
	}
	if len(res.Undo) != 1 || res.Undo[0].Op != "unskip_checkin" {
		t.Fatalf("undo: %+v", res.Undo)
	}
	tomorrow := toolsDay().AddDate(0, 0, 1)
	if c, err := db.Checkin(ctx, tomorrow, "morning"); err != nil || c.Status != "skipped" {
		t.Fatalf("row: %+v, %v", c, err)
	}

	env = nextCall(t, env)
	undo, _ := json.Marshal(res.Undo)
	if err := db.FinishToolCall(ctx, env.ToolCallID, res.Text, undo, "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.ApplyUndo(ctx, db, env.ToolCallID, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Checkin(ctx, tomorrow, "morning"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row not removed: %v", err)
	}

	// Both check-ins, defaulting to today.
	all := runTool(t, env, "skip_checkin", `{"kind":"all"}`)
	if !strings.Contains(all.Text, "morning and evening") {
		t.Fatalf("all: %q", all.Text)
	}
	for _, kind := range []string{"morning", "evening"} {
		if c, err := db.Checkin(ctx, toolsDay(), kind); err != nil || c.Status != "skipped" {
			t.Fatalf("%s: %+v, %v", kind, c, err)
		}
	}
}

func TestSkipCheckinValidation(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "skip_checkin")

	for _, c := range []struct {
		name, args string
		want       error
	}{
		{"past day", `{"day":"yesterday"}`, tools.ErrInvalid},
		{"bad kind", `{"kind":"afternoon"}`, tools.ErrInvalid},
		{"bad day", `{"day":"someday"}`, tools.ErrInvalid},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := tools.New().Run(context.Background(), env, "skip_checkin", c.args); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}
