package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"squad/assistant"
	"squad/llm"
	"squad/store"
	"squad/tools"
)

type memStore struct {
	mu       sync.Mutex
	convs    map[string]store.Conversation
	msgs     []store.Message
	runs     []store.Run
	calls    []store.ToolCall
	nextMsg  int64
	nextRun  int64
	nextCall int64
}

func newMemStore() *memStore { return &memStore{convs: map[string]store.Conversation{}} }

func (m *memStore) CreateConversation(_ context.Context, c store.Conversation) (store.Conversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if got, ok := m.convs[c.ID]; ok {
		return got, nil
	}
	c.CreatedAt, c.UpdatedAt = time.Now(), time.Now()
	m.convs[c.ID] = c
	return c, nil
}

func (m *memStore) GetConversation(_ context.Context, id string) (store.Conversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[id]
	if !ok {
		return store.Conversation{}, store.ErrNotFound
	}
	return c, nil
}

func (m *memStore) ListConversations(_ context.Context, characterID string, limit int) ([]store.Conversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Conversation
	for _, c := range m.convs {
		if c.CharacterID == characterID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) AddMessage(_ context.Context, msg store.Message) (store.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextMsg++
	msg.ID = m.nextMsg
	msg.CreatedAt = time.Now()
	m.msgs = append(m.msgs, msg)
	if c, ok := m.convs[msg.ConversationID]; ok {
		c.UpdatedAt = msg.CreatedAt
		m.convs[msg.ConversationID] = c
	}
	return msg, nil
}

func (m *memStore) Messages(_ context.Context, convID string, after int64, limit int) ([]store.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Message
	for _, msg := range m.msgs {
		if msg.ConversationID == convID && msg.ID > after {
			out = append(out, msg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if limit > 0 && len(out) > limit {
		if after == 0 {
			out = out[len(out)-limit:] // newest page, matching the real store
		} else {
			out = out[:limit]
		}
	}
	return out, nil
}

func (m *memStore) RecentMessagesAfter(ctx context.Context, convID string, after int64, n int) ([]store.Message, error) {
	all, err := m.Messages(ctx, convID, 0, 0)
	if err != nil {
		return nil, err
	}
	var out []store.Message
	for _, msg := range all {
		if msg.ID > after {
			out = append(out, msg)
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

func (m *memStore) RecentMessages(ctx context.Context, convID string, n int) ([]store.Message, error) {
	all, err := m.Messages(ctx, convID, 0, 0)
	if err != nil || n <= 0 || len(all) <= n {
		return all, err
	}
	return all[len(all)-n:], nil
}

func (m *memStore) StartRun(_ context.Context, r store.Run) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextRun++
	r.ID = m.nextRun
	r.StartedAt = time.Now()
	m.runs = append(m.runs, r)
	return r.ID, nil
}

func (m *memStore) FinishRun(_ context.Context, id int64, status, errMsg string, in, out *int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.runs {
		if m.runs[i].ID != id {
			continue
		}
		now := time.Now()
		m.runs[i].Status, m.runs[i].Error = status, errMsg
		m.runs[i].InputTokens, m.runs[i].OutputTokens = in, out
		m.runs[i].EndedAt = &now
	}
	return nil
}

func (m *memStore) StartToolCall(_ context.Context, tc store.ToolCall) (store.ToolCall, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextCall++
	tc.ID = m.nextCall
	tc.CreatedAt = time.Now()
	m.calls = append(m.calls, tc)
	return tc, nil
}

func (m *memStore) FinishToolCall(_ context.Context, id int64, result string, undo []byte, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.calls {
		if m.calls[i].ID == id {
			m.calls[i].Result, m.calls[i].Undo, m.calls[i].Status = result, undo, status
		}
	}
	return nil
}

func (m *memStore) snapshot() (msgs []store.Message, runs []store.Run, calls []store.ToolCall) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.Message(nil), m.msgs...), append([]store.Run(nil), m.runs...), append([]store.ToolCall(nil), m.calls...)
}

func testServer(steps ...llm.FakeStep) (*server, *memStore, *llm.Fake) {
	reg := tools.New()
	reg.Add(&tools.Tool{
		Name: "echo", Description: "echo",
		Parameters: map[string]any{"type": "object"},
		Run: func(_ context.Context, _ tools.Env, raw string) (tools.Result, error) {
			return tools.Result{Text: "ok: " + raw}, nil
		},
	})
	fake := llm.NewFake(steps...)
	st := newMemStore()
	runner := assistant.New(assistant.Config{
		Store: st, Tools: reg,
		Models: map[string]llm.Model{
			string(llm.OpenCodeProvider): fake,
			string(llm.ClaudeProvider):   fake,
		},
		Memory:   func(string) string { return "" },
		Now:      func() time.Time { return time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC) },
		Location: time.UTC,
	})
	chars := map[string]Character{
		"ria": {
			ID: "ria", Name: "Ria", Persona: "You are Ria.", Model: "fake-model",
			Models: []string{"fake-model", "claude/opus"}, Tools: []string{"echo"},
		},
		"claude-char": {
			ID: "claude-char", Name: "Claude", Provider: "claude", Model: "opus", Persona: "You are Claude.",
		},
	}
	return &server{
		chars: chars, list: []Character{chars["ria"], chars["claude-char"]}, db: st, runner: runner,
	}, st, fake
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	return mux
}

func doJSON(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeEvents(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	for dec.More() {
		var e map[string]any
		if err := dec.Decode(&e); err != nil {
			t.Fatalf("ndjson: %v (%q)", err, body)
		}
		out = append(out, e)
	}
	return out
}

func createConversation(t *testing.T, h http.Handler, characterID string) string {
	t.Helper()
	w := doJSON(t, h, "POST", "/api/conversations", `{"characterId":"`+characterID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var c struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || c.ID == "" {
		t.Fatalf("create response: %s (%v)", w.Body, err)
	}
	return c.ID
}

func TestAPICharacters(t *testing.T) {
	s, _, _ := testServer()
	w := doJSON(t, s.handler(), "GET", "/api/characters", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"id":"ria"`) {
		t.Fatalf("body: %s", w.Body)
	}
}

func TestAPIConversationLifecycle(t *testing.T) {
	s, st, fake := testServer(llm.FakeStep{Text: "Hello from fake"}, llm.FakeStep{Text: "Second reply"})
	h := s.handler()

	id := createConversation(t, h, "ria")

	w := doJSON(t, h, "GET", "/api/conversations?characterId=ria", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}

	w = doJSON(t, h, "POST", "/api/conversations/"+id+"/messages", `{"content":"hi"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("post: %d %s", w.Code, w.Body)
	}
	events := decodeEvents(t, w.Body.Bytes())
	if len(events) != 2 || events[0]["type"] != "text" || events[0]["delta"] != "Hello from fake" || events[1]["type"] != "done" {
		t.Fatalf("events: %v", events)
	}

	w = doJSON(t, h, "GET", "/api/conversations/"+id+"/messages", "")
	var got struct {
		Messages []struct {
			ID      int64  `json:"id"`
			Role    string `json:"role"`
			Content string `json:"content"`
			RunID   *int64 `json:"runId"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "user" || got.Messages[1].Role != "assistant" || got.Messages[1].RunID == nil {
		t.Fatalf("messages: %+v", got.Messages)
	}

	msgs, runs, _ := st.snapshot()
	if len(msgs) != 2 || len(runs) != 1 || runs[0].Status != "ok" {
		t.Fatalf("store: %d messages %d runs %+v", len(msgs), len(runs), runs)
	}
	if reqs := fake.Requests(); len(reqs) != 1 || reqs[0].Session != id {
		t.Fatalf("model requests: %+v", reqs)
	}

	// Only the new message travels in the request body; history comes from the store.
	w = doJSON(t, h, "POST", "/api/conversations/"+id+"/messages", `{"content":"again"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("post2: %d %s", w.Code, w.Body)
	}
	reqs := fake.Requests()
	if len(reqs) != 2 || len(reqs[1].Messages) != 3 { // user, assistant, new user
		t.Fatalf("second model request: %+v", reqs[len(reqs)-1].Messages)
	}

	w = doJSON(t, h, "GET", "/api/conversations/"+id+"/messages?limit=1", "")
	if !strings.Contains(w.Body.String(), `"role":"assistant"`) {
		t.Fatalf("limit=1 should return the newest message: %s", w.Body)
	}
}

func TestAPIMessageToolCall(t *testing.T) {
	s, st, _ := testServer(
		llm.FakeStep{ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "echo", Arguments: `{"x":1}`}}},
		llm.FakeStep{Text: "done"},
	)
	h := s.handler()
	id := createConversation(t, h, "ria")

	w := doJSON(t, h, "POST", "/api/conversations/"+id+"/messages", `{"content":"use a tool"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("post: %d %s", w.Code, w.Body)
	}
	events := decodeEvents(t, w.Body.Bytes())
	if events[0]["type"] != "tool" || events[0]["name"] != "echo" || events[1]["type"] != "text" {
		t.Fatalf("events: %v", events)
	}
	_, _, calls := st.snapshot()
	if len(calls) != 1 || calls[0].Name != "echo" || calls[0].Status != "ok" {
		t.Fatalf("tool calls: %+v", calls)
	}

	w = doJSON(t, h, "GET", "/api/conversations/"+id+"/messages", "")
	if !strings.Contains(w.Body.String(), `"role":"tool"`) || !strings.Contains(w.Body.String(), `"toolCallId":"call_1"`) {
		t.Fatalf("messages: %s", w.Body)
	}
}

func TestAPIBadRequests(t *testing.T) {
	s, _, _ := testServer(llm.FakeStep{Text: "x"})
	h := s.handler()
	id := createConversation(t, h, "ria")

	cases := []struct {
		name, body string
		path       string
		want       int
	}{
		{"unknown character", `{"characterId":"nope"}`, "/api/conversations", http.StatusBadRequest},
		{"bad json", `{`, "/api/conversations", http.StatusBadRequest},
		{"unknown conversation post", `{"content":"hi"}`, "/api/conversations/nope/messages", http.StatusNotFound},
		{"unknown conversation get", ``, "/api/conversations/nope/messages", http.StatusNotFound},
		{"bad conversation id", `{"content":"hi"}`, "/api/conversations/bad%20id/messages", http.StatusBadRequest},
		{"empty content", `{"content":"  "}`, "/api/conversations/" + id + "/messages", http.StatusBadRequest},
		{"unknown model", `{"content":"hi","model":"gpt-x"}`, "/api/conversations/" + id + "/messages", http.StatusBadRequest},
		{"bad limit", ``, "/api/conversations/" + id + "/messages?limit=0", http.StatusBadRequest},
		{"unknown list character", ``, "/api/conversations?characterId=nope", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method := "POST"
			if tc.body == "" {
				method = "GET"
			}
			w := doJSON(t, h, method, tc.path, tc.body)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d (%s)", w.Code, tc.want, w.Body)
			}
		})
	}
}

func TestAPIProviderAndModel(t *testing.T) {
	s, _, fake := testServer(llm.FakeStep{Text: "a"}, llm.FakeStep{Text: "b"})
	h := s.handler()

	claudeConv := createConversation(t, h, "claude-char")
	doJSON(t, h, "POST", "/api/conversations/"+claudeConv+"/messages", `{"content":"hi"}`)

	riaConv := createConversation(t, h, "ria")
	doJSON(t, h, "POST", "/api/conversations/"+riaConv+"/messages", `{"content":"hi","model":"claude/opus"}`)

	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: %d", len(reqs))
	}
	if reqs[0].Model != "opus" || reqs[0].Session != claudeConv {
		t.Fatalf("claude route: %+v", reqs[0])
	}
	if reqs[1].Model != "opus" || reqs[1].Session != riaConv {
		t.Fatalf("picker override: %+v", reqs[1])
	}
}

func TestAPIImport(t *testing.T) {
	s, _, _ := testServer()
	h := s.handler()
	body := `{"characterId":"ria","sessionId":"11111111-1111-1111-1111-111111111111",
		"messages":[{"role":"user","content":"old one"},{"role":"assistant","content":"old two"}]}`

	w := doJSON(t, h, "POST", "/api/conversations/import", body)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"imported":true`) {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	id := "11111111-1111-1111-1111-111111111111"
	w = doJSON(t, h, "GET", "/api/conversations/"+id+"/messages", "")
	if !strings.Contains(w.Body.String(), "old one") || !strings.Contains(w.Body.String(), "old two") {
		t.Fatalf("imported messages: %s", w.Body)
	}

	// A retry is a no-op, not a duplicate.
	w = doJSON(t, h, "POST", "/api/conversations/import", body)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"imported":false`) {
		t.Fatalf("second import: %d %s", w.Code, w.Body)
	}
	w = doJSON(t, h, "GET", "/api/conversations/"+id+"/messages", "")
	var got struct {
		Messages []any `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Messages) != 2 {
		t.Fatalf("messages after retry: %s (%v)", w.Body, err)
	}

	// Same session id from another character is a conflict.
	body = strings.Replace(body, `"characterId":"ria"`, `"characterId":"claude-char"`, 1)
	w = doJSON(t, h, "POST", "/api/conversations/import", body)
	if w.Code != http.StatusConflict {
		t.Fatalf("conflict: %d %s", w.Code, w.Body)
	}
}

func TestAPILongConversation(t *testing.T) {
	s, st, fake := testServer(llm.FakeStep{Text: "still here"})
	h := s.handler()
	id := createConversation(t, h, "ria")

	// Seed 500 stored messages, as if the chat had run for months.
	for i := range 500 {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if _, err := st.AddMessage(context.Background(), store.Message{
			ConversationID: id, Role: role, Content: "stored message",
		}); err != nil {
			t.Fatal(err)
		}
	}

	w := doJSON(t, h, "POST", "/api/conversations/"+id+"/messages", `{"content":"new question"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("post: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "still here") {
		t.Fatalf("reply: %s", w.Body)
	}
	if len(st.msgs) != 502 {
		t.Fatalf("stored messages: %d", len(st.msgs))
	}

	req := fake.Requests()[0]
	if len(req.Messages) > 42 { // window (40) + the new input, with a user boundary
		t.Fatalf("model request carried %d messages", len(req.Messages))
	}
	if last := req.Messages[len(req.Messages)-1]; last.Content != "new question" {
		t.Fatalf("last request message: %+v", last)
	}
}
