package assistant

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"characters/llm"
	"characters/store"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata golden files")

type goldenContext struct {
	System     string        `json:"system"`
	Messages   []llm.Message `json:"messages"`
	MessageIDs []int64       `json:"message_ids"`
	Sections   []string      `json:"sections"`
	Window     int           `json:"window"`
}

func golden(c Context) goldenContext {
	return goldenContext{
		System: c.System, Messages: c.Messages, MessageIDs: c.MessageIDs, Sections: c.Sections, Window: c.Window,
	}
}

func TestBuildContextGolden(t *testing.T) {
	char := Character{
		ID: "ria", Name: "Ria",
		Persona: "You are Ria.\nCite sources.",
	}
	memory := "<memory>\nNotes may be outdated. The current messages win.\n</memory>"
	now := time.Date(2026, 12, 1, 9, 30, 0, 0, time.UTC)
	loc := mustLoc("Asia/Makassar")

	history := []store.Message{
		{ID: 1, Role: "user", Content: "first question"},
		{ID: 2, Role: "assistant", Content: "first answer"},
		{ID: 3, Role: "user", Content: "older question"},
		{ID: 4, Role: "assistant", ToolCalls: json.RawMessage(`[{"id":"call_1","name":"web_search","arguments":"{\"query\":\"go\"}"}]`)},
		{ID: 5, Role: "tool", ToolCallID: "call_1", Content: "search results"},
		{ID: 6, Role: "assistant", Content: "older answer"},
		{ID: 7, Role: "user", Content: "new question"},
	}

	planned := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	earlier := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	minutes := 120
	remindAt := time.Date(2026, 12, 1, 15, 0, 0, 0, loc)
	completedAt := time.Date(2026, 11, 30, 14, 0, 0, 0, loc)
	rec := Records{
		Day:        planned,
		Projects:   []string{"assistant", "work"},
		TodayPlans: []store.Task{{ID: 5, Title: "write tests", Status: "open", PlannedOn: &planned}},
		EarlierOpen: []store.Task{
			{ID: 2, Title: "finish the report", Status: "open", PlannedOn: &earlier, TimesMoved: 2},
		},
		TodayActivities: []store.Activity{{ID: 9, Day: planned, Description: "api refactor", Minutes: &minutes}},
		TodayReminders:  []store.Reminder{{ID: 3, Text: "call mom", RemindAt: remindAt, Status: "pending"}},
		Digest: []DaySummary{{
			Day:        time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC),
			Activities: []store.Activity{{ID: 8, Day: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC), Description: "prod bug", Minutes: ptrInt(30)}},
			Completed:  []store.Task{{ID: 4, Title: "ship it", Status: "done", CompletedAt: &completedAt}},
			Slipped:    []store.Task{{ID: 6, Title: "old thing", Status: "open", PlannedOn: &earlier}},
		}},
	}
	got := golden(BuildContext(char, memory, history, 5, now, Settings{Loc: loc, DayStartsAt: 4 * time.Hour}, &rec))
	// Window 5 keeps ids 3..7, which starts on a user turn.
	if len(got.MessageIDs) != 5 || got.MessageIDs[0] != 3 || got.MessageIDs[4] != 7 {
		t.Fatalf("windowed ids: %v", got.MessageIDs)
	}

	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')

	path := filepath.Join("testdata", "context.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run `go test ./assistant -update`): %v", err)
	}
	if string(data) != string(want) {
		t.Fatalf("context changed; got:\n%s\nwant:\n%s", data, want)
	}
}

func TestBuildContextWindow(t *testing.T) {
	loc := mustLoc("UTC")
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	char := Character{ID: "c", Persona: "p"}

	// 500 stored messages: the window keeps the last 40, starting at a user turn.
	var history []store.Message
	for i := int64(1); i <= 500; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		history = append(history, store.Message{ID: i, Role: role, Content: "m"})
	}
	got := BuildContext(char, "", history, DefaultWindow, now, Settings{Loc: loc, DayStartsAt: 4 * time.Hour}, nil)
	if len(got.Messages) != DefaultWindow || got.MessageIDs[0] != 461 || got.MessageIDs[len(got.MessageIDs)-1] != 500 {
		t.Fatalf("window: %d messages, ids %v..%v", len(got.Messages), got.MessageIDs[0], got.MessageIDs[len(got.MessageIDs)-1])
	}
	if got.Messages[0].Role != "user" {
		t.Fatalf("window starts with %q", got.Messages[0].Role)
	}

	// A window that would start inside a tool pair is cut forward to the next
	// user turn; an incomplete assistant/tool pair never reaches the model.
	pair := []store.Message{
		{ID: 1, Role: "user", Content: "u"},
		{ID: 2, Role: "assistant", ToolCalls: json.RawMessage(`[{"id":"c","name":"t","arguments":"{}"}]`)},
		{ID: 3, Role: "tool", ToolCallID: "c", Content: "r"},
	}
	if got := BuildContext(char, "", pair, 2, now, Settings{Loc: loc, DayStartsAt: 4 * time.Hour}, nil); len(got.Messages) != 0 {
		t.Fatalf("dangling tool pair kept: %+v", got.Messages)
	}

	// No history means no history section.
	empty := BuildContext(char, "", nil, 10, now, Settings{Loc: loc, DayStartsAt: 4 * time.Hour}, nil)
	for _, s := range empty.Sections {
		if s == "history" {
			t.Fatalf("empty history section: %v", empty.Sections)
		}
	}
}

func TestBuildContextMemorySection(t *testing.T) {
	loc := mustLoc("UTC")
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	char := Character{ID: "c", Persona: "persona"}

	withMemory := BuildContext(char, "<memory>x</memory>", nil, 10, now, Settings{Loc: loc, DayStartsAt: 4 * time.Hour}, nil)
	if !containsSection(withMemory.Sections, "memory") || !containsSection(withMemory.Sections, "persona") {
		t.Fatalf("sections: %v", withMemory.Sections)
	}
	without := BuildContext(char, "  ", nil, 10, now, Settings{Loc: loc, DayStartsAt: 4 * time.Hour}, nil)
	if containsSection(without.Sections, "memory") {
		t.Fatalf("empty memory section: %v", without.Sections)
	}
}

func ptrInt(v int) *int { return &v }

func TestRecordsPromptCaps(t *testing.T) {
	loc := mustLoc("Asia/Makassar")
	now := time.Date(2026, 12, 1, 9, 30, 0, 0, time.UTC)
	planned := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	older := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

	rec := Records{Day: planned, EarlierOpen: nil, EarlierOpenMore: 3}
	for i := 0; i < maxEarlierOpen; i++ {
		rec.EarlierOpen = append(rec.EarlierOpen, store.Task{
			ID: int64(i + 1), Title: "slipped " + string(rune('a'+i)), Status: "open", PlannedOn: &older,
		})
	}
	rec.TodayActivities = make([]store.Activity, maxTodayActivity+2)
	for i := range rec.TodayActivities {
		rec.TodayActivities[i] = store.Activity{Day: planned, Description: "thing"}
	}
	rec.ActivityMore = 2

	text := rec.Prompt(loc, 4*time.Hour, now)
	if !strings.Contains(text, "and 3 more; call list_tasks") {
		t.Fatalf("earlier-open cap missing:\n%s", text)
	}
	if !strings.Contains(text, "and 2 more; call query_activities") {
		t.Fatalf("activity cap missing:\n%s", text)
	}
	if strings.Contains(text, "Plans for today (") {
		t.Fatalf("empty plans should say none yet:\n%s", text)
	}
}

func TestLoadRecords(t *testing.T) {
	// The real store is covered in store tests; here the mapping and the
	// seven-day window are pinned with a fake.
	day := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	f := &fakeRecords{day: day}
	rec := LoadRecords(context.Background(), f, day, Settings{Loc: mustLoc("Asia/Makassar"), DayStartsAt: 4 * time.Hour})
	if !rec.Day.Equal(day) {
		t.Fatalf("day: %v", rec.Day)
	}
	if len(rec.EarlierOpen) != maxEarlierOpen || rec.EarlierOpenMore != 3 {
		t.Fatalf("earlier open cap: %d, more %d", len(rec.EarlierOpen), rec.EarlierOpenMore)
	}
	if len(rec.Digest) < 2 || !rec.Digest[0].Day.Before(rec.Digest[len(rec.Digest)-1].Day) {
		t.Fatalf("digest order: %+v", rec.Digest)
	}
	for _, d := range rec.Digest {
		if d.Day.Before(day.AddDate(0, 0, -maxDigestDays)) || !d.Day.Before(day) {
			t.Fatalf("digest day outside the window: %v", d.Day)
		}
	}
	if f.from != day.AddDate(0, 0, -maxDigestDays) || f.to != day.AddDate(0, 0, -1) {
		t.Fatalf("digest window: %v..%v", f.from, f.to)
	}
}

// fakeRecords implements RecordsStore with just enough shape to test the
// mapping; nil slices where a section is not under test.
type fakeRecords struct {
	day        time.Time
	from, to   time.Time
	shouldFail bool
}

func (f *fakeRecords) Tasks(_ context.Context, filter store.TaskFilter) ([]store.Task, error) {
	switch {
	case filter.PlannedOn != nil:
		return []store.Task{{ID: 1, Title: "today", PlannedOn: filter.PlannedOn}}, nil
	case filter.PlannedBefore != nil:
		older := f.day.AddDate(0, 0, -2)
		var out []store.Task
		for i := 0; i < maxEarlierOpen+3; i++ {
			out = append(out, store.Task{ID: int64(2 + i), Title: "slipped", PlannedOn: &older})
		}
		return out, nil
	case filter.CompletedFrom != nil:
		at := f.day.AddDate(0, 0, -1).Add(6 * time.Hour)
		return []store.Task{{ID: 5, Title: "done", CompletedAt: &at}}, nil
	case filter.PlannedFrom != nil:
		d1 := *filter.PlannedFrom
		d2 := f.day.AddDate(0, 0, -1)
		return []store.Task{
			{ID: 6, Title: "slipped", PlannedOn: &d1},
			{ID: 7, Title: "dropped", PlannedOn: &d2, Status: "dropped"},
		}, nil
	}
	return nil, nil
}

func (f *fakeRecords) Activities(_ context.Context, filter store.ActivityFilter) ([]store.Activity, error) {
	if filter.From == nil || filter.To == nil {
		return nil, nil
	}
	if filter.From.Equal(*filter.To) {
		return []store.Activity{{Day: *filter.From, Description: "today thing"}}, nil
	}
	f.from, f.to = *filter.From, *filter.To
	return []store.Activity{
		{Day: f.day.AddDate(0, 0, -2), Description: "older thing"},
		{Day: f.day.AddDate(0, 0, -1), Description: "newer thing"},
	}, nil
}

func (f *fakeRecords) Reminders(_ context.Context, _ store.ReminderFilter) ([]store.Reminder, error) {
	return []store.Reminder{{Text: "call mom", RemindAt: f.day.Add(15 * time.Hour)}}, nil
}

func (f *fakeRecords) Projects(_ context.Context) ([]store.Project, error) {
	return []store.Project{{ID: 1, Name: "work"}}, nil
}

func containsSection(sections []string, want string) bool {
	for _, s := range sections {
		if s == want {
			return true
		}
	}
	return false
}
