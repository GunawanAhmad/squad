package store_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"characters/store"
	"characters/store/storetest"
)

func TestConversationsAndMessages(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()

	c, err := s.CreateConversation(ctx, store.Conversation{ID: "conv-1", CharacterID: "ria"})
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "conv-1" || c.CharacterID != "ria" || c.Day != nil {
		t.Fatalf("unexpected conversation: %+v", c)
	}

	// Creating the same id again returns the existing row.
	if _, err := s.CreateConversation(ctx, store.Conversation{ID: "conv-1", CharacterID: "other"}); err != nil {
		t.Fatal(err)
	}
	again, err := s.GetConversation(ctx, "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if again.CharacterID != "ria" {
		t.Fatalf("conversation overwritten: %+v", again)
	}

	if _, err := s.GetConversation(ctx, "nope"); err != store.ErrNotFound {
		t.Fatalf("missing conversation: err = %v, want ErrNotFound", err)
	}

	toolCalls := json.RawMessage(`[{"id":"call_1","name":"web_search","arguments":"{\"query\":\"go\"}"}]`)
	m1, err := s.AddMessage(ctx, store.Message{ConversationID: "conv-1", Role: "user", Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := s.AddMessage(ctx, store.Message{
		ConversationID: "conv-1", Role: "assistant", Content: "hi",
		ToolCalls: toolCalls,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m1.ID == 0 || m2.ID <= m1.ID {
		t.Fatalf("ids not increasing: %d, %d", m1.ID, m2.ID)
	}

	all, err := s.Messages(ctx, "conv-1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Content != "hello" || all[1].Role != "assistant" {
		t.Fatalf("messages: %+v", all)
	}
	var gotCalls, wantCalls any
	if err := json.Unmarshal(all[1].ToolCalls, &gotCalls); err != nil {
		t.Fatalf("tool_calls: %v", err)
	}
	if err := json.Unmarshal(toolCalls, &wantCalls); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotCalls, wantCalls) {
		t.Fatalf("tool_calls not round-tripped: %s", all[1].ToolCalls)
	}
	if all[1].ToolCallID != "" || all[1].ForgottenAt != nil {
		t.Fatalf("nullable fields: %+v", all[1])
	}

	after, err := s.Messages(ctx, "conv-1", m1.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ID != m2.ID {
		t.Fatalf("after %d: %+v", m1.ID, after)
	}

	newest, err := s.Messages(ctx, "conv-1", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(newest) != 1 || newest[0].ID != m2.ID {
		t.Fatalf("newest page: %+v", newest)
	}

	recent, err := s.RecentMessages(ctx, "conv-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].ID != m2.ID {
		t.Fatalf("recent: %+v", recent)
	}

	other, err := s.CreateConversation(ctx, store.Conversation{ID: "conv-2", CharacterID: "ria"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(ctx, store.Message{ConversationID: other.ID, Role: "user", Content: "newer"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListConversations(ctx, "ria", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "conv-2" {
		t.Fatalf("list order: %+v", list)
	}
}

func TestRunsAndToolCalls(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()

	if _, err := s.CreateConversation(ctx, store.Conversation{ID: "c", CharacterID: "ria"}); err != nil {
		t.Fatal(err)
	}
	conv := "c"
	runID, err := s.StartRun(ctx, store.Run{
		Kind: "turn", ConversationID: &conv, Model: "fake",
		Context: json.RawMessage(`{"sections":["persona","now"]}`), Status: "running",
	})
	if err != nil {
		t.Fatal(err)
	}

	in, out := 12, 34
	if err := s.FinishRun(ctx, runID, "ok", "", &in, &out); err != nil {
		t.Fatal(err)
	}
	runs, err := s.Runs(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs: %+v", runs)
	}
	r := runs[0]
	if r.ID != runID || r.Status != "ok" || r.Model != "fake" || r.EndedAt == nil {
		t.Fatalf("run: %+v", r)
	}
	if r.InputTokens == nil || *r.InputTokens != 12 || r.OutputTokens == nil || *r.OutputTokens != 34 {
		t.Fatalf("usage: %+v", r)
	}
	var ctxDoc struct {
		Sections []string `json:"sections"`
	}
	if err := json.Unmarshal(r.Context, &ctxDoc); err != nil || len(ctxDoc.Sections) != 2 {
		t.Fatalf("context: %s (%v)", r.Context, err)
	}

	tc := store.ToolCall{RunID: runID, Name: "web_search", Args: json.RawMessage(`{"query":"go"}`), Result: "results", Status: "ok"}
	tc, err = s.AddToolCall(ctx, tc)
	if err != nil {
		t.Fatal(err)
	}
	if tc.ID == 0 || tc.CreatedAt.IsZero() {
		t.Fatalf("tool call: %+v", tc)
	}
	calls, err := s.ToolCalls(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Name != "web_search" || calls[0].Result != "results" || calls[0].Status != "ok" {
		t.Fatalf("tool calls: %+v", calls)
	}
}

func TestSchemaVersion(t *testing.T) {
	s := storetest.New(t)
	v, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatalf("schema version = %d, want 1", v)
	}
}
