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

// Once embeds one batch of pending messages and facts. A bad row is logged and
// skipped; it must not stop the rest of the batch, the facts, or the next pass.
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

	embeddings := func(rows []store.EmbedRow, set func(r store.EmbedRow, vec []float32) error) int {
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
			if err == nil {
				for i, r := range rows[start:end] {
					if i >= len(vecs) {
						break
					}
					if err := set(r, vecs[i]); err != nil {
						log.Printf("embeddings: row %d: %v", r.ID, err)
						continue
					}
					n++
				}
				continue
			}
			// One bad text must not block the whole batch (and the same rows
			// would be picked again next time). Retry each row alone.
			for _, r := range rows[start:end] {
				v, e := w.Embedder.Embed(ctx, []string{clip(r.Content, maxEmbedRunes)})
				if e != nil || len(v) != 1 {
					log.Printf("embeddings: row %d: %v", r.ID, e)
					continue
				}
				if err := set(r, v[0]); err != nil {
					log.Printf("embeddings: row %d: %v", r.ID, err)
					continue
				}
				n++
			}
		}
		return n
	}

	msgs, err := w.Store.UnembeddedMessages(ctx, perRun)
	if err != nil {
		return done, err
	}
	done += embeddings(msgs, func(r store.EmbedRow, vec []float32) error {
		return w.Store.SetMessageEmbedding(ctx, r.ID, vec, w.Embedder.Model())
	})

	facts, err := w.Store.UnembeddedFacts(ctx, perRun)
	if err != nil {
		return done, err
	}
	done += embeddings(facts, func(r store.EmbedRow, vec []float32) error {
		return w.Store.SetFactEmbedding(ctx, r.ID, r.Content, vec, w.Embedder.Model())
	})
	return done, nil
}
