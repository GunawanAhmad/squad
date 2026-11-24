package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"characters/assistant"
	"characters/store"
	"characters/store/storetest"
	"characters/tools"
)

// fixed clock: 2026-12-01 09:00 in Asia/Makassar (UTC+8), so the logical day is
// 2026-12-01 and "tomorrow" is 2026-12-02.
var testNow = time.Date(2026, 12, 1, 1, 0, 0, 0, time.UTC)

func newRecordServer(t *testing.T) (*server, *store.Store, *http.ServeMux) {
	t.Helper()
	db := storetest.New(t)
	s := &server{
		chars: map[string]Character{"assistant": {ID: "assistant", Name: "Assistant"}},
		db:    db, records: db, now: func() time.Time { return testNow },
	}
	mux := http.NewServeMux()
	s.routes(mux)
	return s, db, mux
}

func do(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestRecordsTasksAPI(t *testing.T) {
	_, db, mux := newRecordServer(t)
	ctx := context.Background()
	settings := assistant.DefaultSettings()
	day := assistant.LogicalDay(testNow, settings.Loc, settings.DayStartsAt)

	// Created through the same validation the tools use.
	task, err := tools.CreateTask(ctx, db, "write the tests", "", "", "", day, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	list := decode[struct {
		Tasks []map[string]any `json:"tasks"`
	}](t, do(t, mux, "GET", "/api/assistant/tasks?plannedOn=2026-12-01", nil))
	if len(list.Tasks) != 1 || list.Tasks[0]["title"] != "write the tests" || list.Tasks[0]["plannedOn"] != "2026-12-01" {
		t.Fatalf("tasks: %+v", list)
	}

	rec := do(t, mux, "PATCH", "/api/assistant/tasks/"+itoa(task.ID), map[string]any{"plannedOn": "tomorrow"})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	patched := decode[map[string]any](t, rec)
	if patched["plannedOn"] != "2026-12-02" || patched["timesMoved"] != float64(1) {
		t.Fatalf("patched task: %+v", patched)
	}

	// A date the model cannot write is not accepted from the panel either.
	if rec := do(t, mux, "PATCH", "/api/assistant/tasks/"+itoa(task.ID), map[string]any{"plannedOn": "2026-13-40"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad date: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, mux, "PATCH", "/api/assistant/tasks/"+itoa(task.ID), map[string]any{"status": "later"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, mux, "PATCH", "/api/assistant/tasks/"+itoa(task.ID), map[string]any{"status": "done"})
	if rec.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	if done := decode[map[string]any](t, rec); done["completedAt"] == nil {
		t.Fatalf("completed task: %+v", done)
	}

	if rec := do(t, mux, "DELETE", "/api/assistant/tasks/"+itoa(task.ID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, mux, "DELETE", "/api/assistant/tasks/"+itoa(task.ID), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete twice: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecordsActivitiesAndRemindersAPI(t *testing.T) {
	_, db, mux := newRecordServer(t)
	ctx := context.Background()
	settings := assistant.DefaultSettings()
	day := assistant.LogicalDay(testNow, settings.Loc, settings.DayStartsAt)

	minutes := 90
	act, err := tools.AddActivity(ctx, db, "api refactor", &minutes, "", "", day, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	list := decode[struct {
		Activities []map[string]any `json:"activities"`
	}](t, do(t, mux, "GET", "/api/assistant/activities?from=2026-12-01&to=2026-12-01", nil))
	if len(list.Activities) != 1 || list.Activities[0]["minutes"] != float64(90) {
		t.Fatalf("activities: %+v", list)
	}
	if rec := do(t, mux, "PATCH", "/api/assistant/activities/"+itoa(act.ID), map[string]any{"minutes": 5000}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad minutes: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(t, mux, "PATCH", "/api/assistant/activities/"+itoa(act.ID), map[string]any{"clearMinutes": true})
	if rec.Code != http.StatusOK || decode[map[string]any](t, rec)["minutes"] != nil {
		t.Fatalf("clear minutes: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, mux, "DELETE", "/api/assistant/activities/"+itoa(act.ID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete activity: %d %s", rec.Code, rec.Body.String())
	}

	// 15:00 today in Asia/Makassar.
	rem, err := tools.AddReminder(ctx, db, "call mom", "15:00", "daily", testNow, settings.Loc, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rem.RemindAt.In(settings.Loc).Format("15:04") != "15:00" {
		t.Fatalf("reminder time: %v", rem.RemindAt)
	}
	if _, err := tools.AddReminder(ctx, db, "too late", "07:00", "", testNow, settings.Loc, nil, nil); err == nil {
		t.Fatal("a past reminder must be rejected")
	}

	reminders := decode[struct {
		Reminders []map[string]any `json:"reminders"`
	}](t, do(t, mux, "GET", "/api/assistant/reminders?status=pending", nil))
	if len(reminders.Reminders) != 1 || reminders.Reminders[0]["repeat"] != "daily" {
		t.Fatalf("reminders: %+v", reminders)
	}
	rec = do(t, mux, "PATCH", "/api/assistant/reminders/"+itoa(rem.ID), map[string]any{"cancel": true})
	if rec.Code != http.StatusOK || decode[map[string]any](t, rec)["status"] != "cancelled" {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, mux, "PATCH", "/api/assistant/reminders/"+itoa(rem.ID), map[string]any{"at": "07:00"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("past time: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, mux, "DELETE", "/api/assistant/reminders/"+itoa(rem.ID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete reminder: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecordsSettingsAPI(t *testing.T) {
	_, _, mux := newRecordServer(t)

	got := decode[map[string]any](t, do(t, mux, "GET", "/api/assistant/settings", nil))
	if got["morningAt"] != "08:00" || got["eveningAt"] != "21:00" || got["dayStartsAt"] != "04:00" || got["tz"] != "Asia/Makassar" {
		t.Fatalf("defaults: %+v", got)
	}

	rec := do(t, mux, "PUT", "/api/assistant/settings", map[string]any{
		"morningAt": "07:30", "eveningAt": "22:15", "tz": "Asia/Jakarta",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put settings: %d %s", rec.Code, rec.Body.String())
	}
	got = decode[map[string]any](t, rec)
	if got["morningAt"] != "07:30" || got["eveningAt"] != "22:15" || got["tz"] != "Asia/Jakarta" {
		t.Fatalf("saved settings: %+v", got)
	}
	if rec := do(t, mux, "PUT", "/api/assistant/settings", map[string]any{"tz": "Mars/Olympus"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad tz: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, mux, "PUT", "/api/assistant/settings", map[string]any{"morningAt": "8am"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad time: %d %s", rec.Code, rec.Body.String())
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
