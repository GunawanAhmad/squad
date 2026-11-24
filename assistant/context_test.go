package assistant

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
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

	got := golden(BuildContext(char, memory, now, loc, history, 5))
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
	got := BuildContext(char, "", now, loc, history, DefaultWindow)
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
	if got := BuildContext(char, "", now, loc, pair, 2); len(got.Messages) != 0 {
		t.Fatalf("dangling tool pair kept: %+v", got.Messages)
	}

	// No history means no history section.
	empty := BuildContext(char, "", now, loc, nil, 10)
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

	withMemory := BuildContext(char, "<memory>x</memory>", now, loc, nil, 10)
	if !containsSection(withMemory.Sections, "memory") || !containsSection(withMemory.Sections, "persona") {
		t.Fatalf("sections: %v", withMemory.Sections)
	}
	without := BuildContext(char, "  ", now, loc, nil, 10)
	if containsSection(without.Sections, "memory") {
		t.Fatalf("empty memory section: %v", without.Sections)
	}
}

func containsSection(sections []string, want string) bool {
	for _, s := range sections {
		if s == want {
			return true
		}
	}
	return false
}
