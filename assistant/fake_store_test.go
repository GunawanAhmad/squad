package assistant

import (
	"context"
	"sync"
	"time"

	"characters/store"
)

// fakeStore implements assistant.Store in memory, so the loop tests need no
// Postgres. The real store is covered by store/store_test.go and by
// TestTurnPersistsToPostgres.
type fakeStore struct {
	mu       sync.Mutex
	convs    map[string]store.Conversation
	msgs     []store.Message
	runs     []store.Run
	calls    []store.ToolCall
	nextMsg  int64
	nextRun  int64
	nextCall int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{convs: map[string]store.Conversation{}}
}

func (f *fakeStore) CreateConversation(_ context.Context, c store.Conversation) (store.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if got, ok := f.convs[c.ID]; ok {
		return got, nil
	}
	c.CreatedAt, c.UpdatedAt = time.Now(), time.Now()
	f.convs[c.ID] = c
	return c, nil
}

func (f *fakeStore) AddMessage(_ context.Context, m store.Message) (store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextMsg++
	m.ID = f.nextMsg
	m.CreatedAt = time.Now()
	f.msgs = append(f.msgs, m)
	return m, nil
}

func (f *fakeStore) RecentMessages(_ context.Context, conversationID string, n int) ([]store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []store.Message
	for _, m := range f.msgs {
		if m.ConversationID == conversationID {
			all = append(all, m)
		}
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

func (f *fakeStore) StartRun(_ context.Context, r store.Run) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextRun++
	r.ID = f.nextRun
	r.StartedAt = time.Now()
	f.runs = append(f.runs, r)
	return r.ID, nil
}

func (f *fakeStore) FinishRun(_ context.Context, id int64, status, errMsg string, inputTokens, outputTokens *int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.runs {
		if f.runs[i].ID != id {
			continue
		}
		now := time.Now()
		f.runs[i].Status = status
		f.runs[i].Error = errMsg
		f.runs[i].InputTokens = inputTokens
		f.runs[i].OutputTokens = outputTokens
		f.runs[i].EndedAt = &now
	}
	return nil
}

func (f *fakeStore) StartToolCall(_ context.Context, tc store.ToolCall) (store.ToolCall, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextCall++
	tc.ID = f.nextCall
	tc.CreatedAt = time.Now()
	tc.Status = "running"
	f.calls = append(f.calls, tc)
	return tc, nil
}

func (f *fakeStore) FinishToolCall(_ context.Context, id int64, result string, undo []byte, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.calls {
		if f.calls[i].ID == id {
			f.calls[i].Result, f.calls[i].Undo, f.calls[i].Status = result, undo, status
		}
	}
	return nil
}

func (f *fakeStore) messages() []store.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Message(nil), f.msgs...)
}

func (f *fakeStore) runList() []store.Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Run(nil), f.runs...)
}

func (f *fakeStore) toolCalls() []store.ToolCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.ToolCall(nil), f.calls...)
}
