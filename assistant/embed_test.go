package assistant

import (
	"context"
	"testing"
	"time"

	"characters/store"
	"characters/store/storetest"
)

type fakeEmbedder struct {
	model string
	calls int
}

func (f *fakeEmbedder) Model() string { return f.model }
func (f *fakeEmbedder) Dims() int     { return 1024 }
func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, 1024)
		v[0] = 1
		out[i] = v
	}
	return out, nil
}

func TestEmbedWorkerOnce(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.CreateConversation(ctx, store.Conversation{ID: "c", CharacterID: "assistant"}); err != nil {
		t.Fatal(err)
	}
	long, err := db.AddMessage(ctx, store.Message{ConversationID: "c", Role: "user", Content: "a message long enough to be embedded"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddMessage(ctx, store.Message{ConversationID: "c", Role: "user", Content: "short"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddMessage(ctx, store.Message{ConversationID: "c", Role: "tool", Content: "a tool result that is definitely long enough"}); err != nil {
		t.Fatal(err)
	}
	fact, err := db.AddFact(ctx, store.Fact{Text: "prefers tea", Category: "preference", ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}

	emb := &fakeEmbedder{model: "fake"}
	w := &EmbedWorker{Store: db, Embedder: emb}
	n, err := w.Once(ctx)
	if err != nil || n != 2 {
		t.Fatalf("once: %d, %v", n, err)
	}
	if emb.calls != 2 {
		t.Fatalf("embedder calls: %d (one batch of messages, one of facts)", emb.calls)
	}
	if m, f, err := db.EmbeddedCounts(ctx, "fake"); err != nil || m != 1 || f != 1 {
		t.Fatalf("counts: %d, %d, %v", m, f, err)
	}
	// Nothing left: the next pass does no work.
	if n, err := w.Once(ctx); err != nil || n != 0 || emb.calls != 2 {
		t.Fatalf("second pass: %d, %v, calls %d", n, err, emb.calls)
	}
	_ = long
	_ = fact
}
