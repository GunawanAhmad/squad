package store

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Embedding storage and hybrid (keyword + vector) search. Vectors come from a
// local bge-m3 via Ollama; embedding_model records which model made a vector, so
// mixed sources are never compared. Exact cosine scan: correct with filters.

// EmbedRow is one row waiting for (or carrying) an embedding.
type EmbedRow struct {
	ID      int64
	Content string
}

func (s *Store) UnembeddedMessages(ctx context.Context, limit int) ([]EmbedRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, content FROM messages
		WHERE embedding IS NULL AND forgotten_at IS NULL
		  AND role IN ('user','assistant') AND length(content) >= 20
		ORDER BY id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEmbedRows(rows)
}

func (s *Store) UnembeddedFacts(ctx context.Context, limit int) ([]EmbedRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, text FROM facts
		WHERE embedding IS NULL AND status <> 'forgotten'
		ORDER BY id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEmbedRows(rows)
}

func scanEmbedRows(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]EmbedRow, error) {
	var out []EmbedRow
	for rows.Next() {
		var r EmbedRow
		if err := rows.Scan(&r.ID, &r.Content); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SetMessageEmbedding(ctx context.Context, id int64, vec []float32, model string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE messages SET embedding = $2::vector, embedding_model = $3 WHERE id = $1`,
		id, FormatVector(vec), model)
	return err
}

func (s *Store) SetFactEmbedding(ctx context.Context, id int64, vec []float32, model string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE facts SET embedding = $2::vector, embedding_model = $3 WHERE id = $1`,
		id, FormatVector(vec), model)
	return err
}

// ClearFactEmbedding forces a re-embed after the text changes.
func (s *Store) ClearFactEmbedding(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE facts SET embedding = NULL, embedding_model = NULL WHERE id = $1`, id)
	return err
}

// EmbeddedCounts reports progress (messages, facts) for measurement.
func (s *Store) EmbeddedCounts(ctx context.Context, model string) (int, int, error) {
	var messages, facts int
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM messages WHERE embedding IS NOT NULL AND embedding_model = $1),
			(SELECT count(*) FROM facts WHERE embedding IS NOT NULL AND embedding_model = $1)`, model).
		Scan(&messages, &facts)
	return messages, facts, err
}

// EmbeddingCorpus returns real messages for the M-10 measurement (read-only).
func (s *Store) EmbeddingCorpus(ctx context.Context, limit int) ([]EmbedRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, content FROM messages
		WHERE forgotten_at IS NULL AND role IN ('user','assistant') AND length(content) >= 20
		ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEmbedRows(rows)
}

// FormatVector is the text form pgvector accepts for $n::vector.
func FormatVector(vec []float32) string {
	parts := make([]string, len(vec))
	for i, v := range vec {
		parts[i] = strconv.FormatFloat(float64(v), 'f', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// mergeRRF merges keyword and vector result lists with reciprocal rank fusion.
// A row found by both ranks better than one found by either alone.
func mergeRRF[T any](keyword, vector []T, id func(T) int64, limit int) []T {
	const k = 60.0
	scores := map[int64]float64{}
	byID := map[int64]T{}
	order := map[int64]int{}
	next := 0
	note := func(list []T) {
		for i, item := range list {
			key := id(item)
			if _, ok := byID[key]; !ok {
				order[key] = next
				next++
			}
			byID[key] = item
			scores[key] += 1 / (k + float64(i+1))
		}
	}
	note(keyword)
	note(vector)
	out := make([]T, 0, len(byID))
	for key := range byID {
		out = append(out, byID[key])
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := id(out[i]), id(out[j])
		if scores[a] != scores[b] {
			return scores[a] > scores[b]
		}
		return order[a] < order[b]
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// searchMessagesVector returns the nearest messages by cosine distance.
func (s *Store) searchMessagesVector(ctx context.Context, q MessageQuery) ([]MessageHit, error) {
	if len(q.Vector) == 0 || q.EmbeddingModel == "" {
		return nil, nil
	}
	args := []any{FormatVector(q.Vector), q.EmbeddingModel}
	where := `m.forgotten_at IS NULL AND m.embedding IS NOT NULL AND m.embedding_model = $2`
	if q.From != nil || q.To != nil {
		tz := q.TZ
		if tz == "" {
			tz = "UTC"
		}
		args = append(args, tz)
		dayExpr := fmt.Sprintf("COALESCE(c.day, (m.created_at AT TIME ZONE $%d)::date)", len(args))
		if q.From != nil {
			args = append(args, Day(*q.From))
			where += fmt.Sprintf(" AND %s >= $%d::date", dayExpr, len(args))
		}
		if q.To != nil {
			args = append(args, Day(*q.To))
			where += fmt.Sprintf(" AND %s <= $%d::date", dayExpr, len(args))
		}
	}
	if len(q.Exclude) > 0 {
		args = append(args, q.Exclude)
		where += fmt.Sprintf(" AND m.id <> ALL($%d)", len(args))
	}
	limit := q.Limit
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT m.id, m.conversation_id, c.character_id, c.day, m.role, m.content, m.created_at,
		       (1 - (m.embedding <=> $1::vector))::float8 AS score
		FROM messages m JOIN conversations c ON c.id = m.conversation_id
		WHERE %s
		ORDER BY m.embedding <=> $1::vector
		LIMIT $%d`, where, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageHit
	for rows.Next() {
		var h MessageHit
		if err := rows.Scan(&h.ID, &h.ConversationID, &h.CharacterID, &h.Day, &h.Role,
			&h.Content, &h.CreatedAt, &h.Rank); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// searchFactsVector is the fact equivalent, including superseded facts so a
// date-filtered semantic query can find what used to be true.
func (s *Store) searchFactsVector(ctx context.Context, q FactQuery) ([]Fact, error) {
	if len(q.Vector) == 0 || q.EmbeddingModel == "" {
		return nil, nil
	}
	args := []any{FormatVector(q.Vector), q.EmbeddingModel}
	where := `status <> 'forgotten' AND embedding IS NOT NULL AND embedding_model = $2`
	if q.From != nil {
		args = append(args, Day(*q.From))
		where += fmt.Sprintf(" AND (valid_to IS NULL OR valid_to >= $%d::date)", len(args))
	}
	if q.To != nil {
		args = append(args, Day(*q.To))
		where += fmt.Sprintf(" AND valid_from <= $%d::date", len(args))
	}
	if q.From == nil && q.To == nil {
		where += " AND status = 'active'"
	}
	limit := q.Limit
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM facts
		WHERE %s
		ORDER BY embedding <=> $1::vector
		LIMIT $%d`, factCols, where, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
