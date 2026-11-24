package tools_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"characters/store"
	"characters/store/storetest"
	"characters/tools"
)

func TestLogAndQueryActivities(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "log_activity", "query_activities")

	res := runTool(t, env, "log_activity", `{"description":"api refactor","minutes":120,"project":"assistant"}`)
	if !strings.Contains(res.Text, "logged on 2026-12-01") || !strings.Contains(res.Text, "120 min") {
		t.Fatalf("log: %q", res.Text)
	}
	if len(res.Undo) != 1 || res.Undo[0].Op != "delete_activity" {
		t.Fatalf("undo: %+v", res.Undo)
	}

	// Minutes are only stored when said: this entry has none.
	runTool(t, env, "log_activity", `{"description":"gym"}`)
	// Yesterday, so the day filter has something to exclude.
	runTool(t, env, "log_activity", `{"description":"prod bug","minutes":30,"day":"yesterday"}`)

	today := runTool(t, env, "query_activities", `{}`)
	if !strings.Contains(today.Text, "api refactor (120 min)") || !strings.Contains(today.Text, "gym") {
		t.Fatalf("today: %q", today.Text)
	}
	if strings.Contains(today.Text, "prod bug") {
		t.Fatalf("yesterday leaked into today: %q", today.Text)
	}
	if !strings.Contains(today.Text, "total: 2 entries, 120 minutes recorded (1 without minutes)") {
		t.Fatalf("totals: %q", today.Text)
	}

	both := runTool(t, env, "query_activities", `{"from":"yesterday","to":"today"}`)
	if !strings.Contains(both.Text, "2026-11-30:") || !strings.Contains(both.Text, "total: 3 entries, 150 minutes recorded") {
		t.Fatalf("range: %q", both.Text)
	}

}

func TestLogActivityLinksATask(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "plan_tasks", "log_activity")

	runTool(t, env, "plan_tasks", `{"tasks":[{"title":"finish the report"}]}`)
	res := runTool(t, env, "log_activity", `{"description":"finished the report","minutes":45,"task":"finish the report"}`)
	if !strings.Contains(res.Text, "linked to #1 finish the report") {
		t.Fatalf("link: %q", res.Text)
	}
	rows, err := db.Activities(context.Background(), store.ActivityFilter{})
	if err != nil || len(rows) != 1 || rows[0].TaskID == nil || *rows[0].TaskID != 1 {
		t.Fatalf("activity task link: %+v, %v", rows, err)
	}

	// An unknown task does not lose the log entry; it is reported instead.
	res = runTool(t, env, "log_activity", `{"description":"something else","task":"no such task"}`)
	if !strings.Contains(res.Text, "logged on") || !strings.Contains(res.Text, "task not linked") {
		t.Fatalf("unlinked: %q", res.Text)
	}
}

func TestLogActivityValidation(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "log_activity", "query_activities")

	cases := []struct {
		name, args string
		want       error
	}{
		{"no description", `{"description":" "}`, tools.ErrInvalid},
		{"zero minutes", `{"description":"x","minutes":0}`, tools.ErrInvalid},
		{"too many minutes", `{"description":"x","minutes":1441}`, tools.ErrInvalid},
		{"bad day", `{"description":"x","day":"someday"}`, tools.ErrInvalid},
		{"bad range", `{"from":"tomorrow","to":"yesterday"}`, tools.ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tool := "log_activity"
			if strings.HasPrefix(c.args, `{"from"`) {
				tool = "query_activities"
			}
			if _, err := tools.New().Run(context.Background(), env, tool, c.args); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestQueryActivitiesProjectFilterAndEmpty(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "log_activity", "query_activities")

	runTool(t, env, "log_activity", `{"description":"api refactor","minutes":60,"project":"work"}`)
	runTool(t, env, "log_activity", `{"description":"gym","minutes":30}`)

	work := runTool(t, env, "query_activities", `{"project":"work"}`)
	if !strings.Contains(work.Text, "api refactor") || strings.Contains(work.Text, "gym") {
		t.Fatalf("project filter: %q", work.Text)
	}

	none := runTool(t, env, "query_activities", `{"from":"2026-11-01","to":"2026-11-02"}`)
	if !strings.Contains(none.Text, "no activities recorded") {
		t.Fatalf("empty range: %q", none.Text)
	}

	rows, err := db.Activities(context.Background(), store.ActivityFilter{})
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows: %+v, %v", rows, err)
	}
}
