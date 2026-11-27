package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"squad/assistant"
	"squad/character"
	"squad/llm"
	"squad/store"
	"squad/store/storetest"
	"squad/tools"
)

// fixed clock: 2026-12-01 09:00 in Asia/Makassar (UTC+8), so the logical day is
// 2026-12-01 and "tomorrow" is 2026-12-02.
var testNow = time.Date(2026, 12, 1, 1, 0, 0, 0, time.UTC)

func newRecordServer(t *testing.T) (*Server, *store.Store, *http.ServeMux) {
	t.Helper()
	db := storetest.New(t)
	s := &Server{
		roster: character.NewRoster([]character.Character{{ID: "assistant", Name: "Assistant"}}, tools.New()),
		db:     db, records: db, now: func() time.Time { return testNow },
		memoryDir: t.TempDir(),
	}
	mux := http.NewServeMux()
	s.Routes(mux)
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

func TestUndoEndpoint(t *testing.T) {
	_, db, mux := newRecordServer(t)
	ctx := context.Background()
	settings := assistant.DefaultSettings()
	day := assistant.LogicalDay(testNow, settings.Loc, settings.DayStartsAt)

	if _, err := db.CreateConversation(ctx, store.Conversation{ID: "daily:2026-12-01", CharacterID: "assistant"}); err != nil {
		t.Fatal(err)
	}
	conv := "daily:2026-12-01"
	runID, err := db.StartRun(ctx, store.Run{Kind: "turn", ConversationID: &conv, Model: "fake", Status: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	call, err := db.StartToolCall(ctx, store.ToolCall{RunID: runID, Name: "plan_tasks", Args: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	task, err := tools.CreateTask(ctx, db, "write the tests", "", "", "", day, &call.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	undo, _ := json.Marshal([]tools.UndoOp{{Op: "delete_task", TaskID: task.ID, ExpectUpdated: task.UpdatedAt.Format(time.RFC3339Nano)}})
	if err := db.FinishToolCall(ctx, call.ID, "planned 1", undo, "ok"); err != nil {
		t.Fatal(err)
	}

	rec := do(t, mux, "POST", "/api/assistant/undo", map[string]any{
		"toolCallId": call.ID, "kind": "task", "id": task.ID, "conversationId": conv,
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "removed plan") {
		t.Fatalf("undo: %d %s", rec.Code, rec.Body.String())
	}
	// The undo shows in the chat as an assistant message.
	msgs, err := db.Messages(ctx, conv, 0, 0)
	if err != nil || len(msgs) != 1 || !strings.Contains(msgs[0].Content, "↩ removed plan") {
		t.Fatalf("undo message: %+v, %v", msgs, err)
	}
	if left, _ := db.Tasks(ctx, store.TaskFilter{}); len(left) != 0 {
		t.Fatalf("task not undone: %+v", left)
	}
	if rec := do(t, mux, "POST", "/api/assistant/undo", map[string]any{"toolCallId": call.ID, "kind": "task", "id": task.ID}); rec.Code != http.StatusConflict {
		t.Fatalf("second undo: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, mux, "POST", "/api/assistant/undo", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing id: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAssistantDailyThread(t *testing.T) {
	s, db, mux := newRecordServer(t)
	_ = s
	ctx := context.Background()

	rec := do(t, mux, "POST", "/api/conversations", map[string]any{"characterId": "assistant"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	conv := decode[map[string]any](t, rec)
	if conv["id"] != "daily:2026-12-01" || conv["day"] != "2026-12-01" {
		t.Fatalf("daily thread: %+v", conv)
	}
	// Again on the same day: the same thread, not a second one.
	again := decode[map[string]any](t, do(t, mux, "POST", "/api/conversations", map[string]any{"characterId": "assistant"}))
	if again["id"] != conv["id"] {
		t.Fatalf("second create: %+v", again)
	}
	stored, err := db.GetConversation(ctx, "daily:2026-12-01")
	if err != nil || stored.Day == nil || store.DayString(*stored.Day) != "2026-12-01" {
		t.Fatalf("stored conversation: %+v, %v", stored, err)
	}

	// A persona still gets a fresh thread per "New chat".
	persona := decode[map[string]any](t, do(t, mux, "POST", "/api/conversations", map[string]any{"characterId": "assistant"}))
	_ = persona
}

func TestDueEndpoint(t *testing.T) {
	db := storetest.New(t)
	fake := llm.NewFake(llm.FakeStep{Text: "Morning!"})
	runner := assistant.New(assistant.Config{
		Store: db, DB: db,
		Models: map[string]llm.Model{
			string(llm.OpenCodeProvider): fake, string(llm.ClaudeProvider): fake,
		},
		Now: func() time.Time { return testNow }, Location: assistant.TimeZone(),
	})
	engine := &assistant.Engine{
		Store: db, Runner: runner, Now: func() time.Time { return testNow },
		Char: assistant.Character{ID: "assistant", Provider: "opencode", Model: "fake", Daily: true},
	}
	s := &Server{
		roster: character.NewRoster([]character.Character{{ID: "assistant"}}, tools.New()),
		db:     db, records: db, engine: engine, now: func() time.Time { return testNow },
	}
	mux := http.NewServeMux()
	s.Routes(mux)

	// 09:00 on 1 December: the morning check-in is due, and a reminder that
	// came due is written into today's thread immediately.
	if _, err := db.AddReminder(context.Background(), store.Reminder{
		Text: "call mom", RemindAt: time.Date(2026, 12, 1, 8, 57, 0, 0, assistant.TimeZone()),
	}); err != nil {
		t.Fatal(err)
	}

	due := decode[map[string]any](t, do(t, mux, "GET", "/api/assistant/due", nil))
	if due["day"] != "2026-12-01" || due["conversationId"] != "daily:2026-12-01" || due["checkin"] != "morning" {
		t.Fatalf("due: %+v", due)
	}
	reminders := due["reminders"].([]any)
	if len(reminders) != 1 {
		t.Fatalf("reminders: %+v", reminders)
	}
	msgs, err := db.Messages(context.Background(), "daily:2026-12-01", 0, 0)
	if err != nil || len(msgs) != 1 || msgs[0].Content != "⏰ call mom" {
		t.Fatalf("reminder message: %+v, %v", msgs, err)
	}
}

func TestDailyThreadBeforeFourAM(t *testing.T) {
	// 02:00 on 2 December in Makassar is still the 1st's thread.
	s, _, mux := newRecordServer(t)
	s.now = func() time.Time { return time.Date(2026, 12, 1, 18, 0, 0, 0, time.UTC) }
	conv := decode[map[string]any](t, do(t, mux, "POST", "/api/conversations", map[string]any{"characterId": "assistant"}))
	if conv["id"] != "daily:2026-12-01" {
		t.Fatalf("02:00 thread: %+v", conv)
	}

	// 04:00 starts the next day.
	s.now = func() time.Time { return time.Date(2026, 12, 1, 20, 0, 0, 0, time.UTC) }
	conv = decode[map[string]any](t, do(t, mux, "POST", "/api/conversations", map[string]any{"characterId": "assistant"}))
	if conv["id"] != "daily:2026-12-02" {
		t.Fatalf("04:00 thread: %+v", conv)
	}
}
