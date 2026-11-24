package assistant

import (
	"context"
	"log"
	"time"

	"characters/llm"
	"characters/store"
)

// EmbedWorker fills embedding columns in small batches. It skips tool/trigger
// rows, very short messages and forgotten rows (the store query does that), and
// it is safe to run while the app is used: a failed batch simply waits for the
// next tick and keyword search keeps working.

type EmbedWorker struct {
	Store    *store.Store
	Embedder llm.Embedder
	Batch    int
	Interval time.Duration
	// PerRun caps how many rows one pass handles, so a back-fill never hogs
	// the machine.
	PerRun int
}

const maxEmbedRunes = 8000 // bge-m3's context limit; longer text is cut

func (w *EmbedWorker) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := w.Once(ctx); err != nil {
				log.Printf("embeddings: %v", err)
			} else if n > 0 {
				log.Printf("embeddings: %d rows embedded", n)
			}
		}
	}
}

// Once embeds one batch of pending messages and facts.
func (w *EmbedWorker) Once(ctx context.Context) (int, error) {
	if w.Store == nil || w.Embedder == nil {
		return 0, nil
	}
	batch := w.Batch
	if batch <= 0 {
		batch = 16
	}
	perRun := w.PerRun
	if perRun <= 0 {
		perRun = 8 * batch
	}
	done := 0

	embeddings := func(rows []store.EmbedRow, set func(id int64, vec []float32) error) (int, error) {
		n := 0
		for start := 0; start < len(rows) && n < perRun; start += batch {
			end := start + batch
			if end > len(rows) {
				end = len(rows)
			}
			texts := make([]string, 0, end-start)
			for _, r := range rows[start:end] {
				texts = append(texts, clip(r.Content, maxEmbedRunes))
			}
			vecs, err := w.Embedder.Embed(ctx, texts)
			if err != nil {
				return n, err
			}
			for i, r := range rows[start:end] {
				if err := set(r.ID, vecs[i]); err != nil {
					return n, err
				}
				n++
			}
		}
		return n, nil
	}

	msgs, err := w.Store.UnembeddedMessages(ctx, perRun)
	if err != nil {
		return done, err
	}
	n, err := embeddings(msgs, func(id int64, vec []float32) error {
		return w.Store.SetMessageEmbedding(ctx, id, vec, w.Embedder.Model())
	})
	done += n
	if err != nil {
		return done, err
	}

	facts, err := w.Store.UnembeddedFacts(ctx, perRun)
	if err != nil {
		return done, err
	}
	n, err = embeddings(facts, func(id int64, vec []float32) error {
		return w.Store.SetFactEmbedding(ctx, id, vec, w.Embedder.Model())
	})
	return done + n, err
}
