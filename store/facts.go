package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Fact is one durable thing about the user. Facts are never overwritten:
// superseding closes valid_to on the old row and links it to the new one, so a
// period question still finds it. Forget() keeps the row, marked forgotten.
type Fact struct {
	ID              int64
	Text            string
	Category        string // preference | person | circumstance | goal | work | health | general
	Core            bool
	Status          string // active | superseded | forgotten
	SourceKind      string // user | tool | extraction
	SourceMessageID *int64
	SupersededBy    *int64
	ValidFrom       time.Time // date
	ValidTo         *time.Time
	LastUsedAt      *time.Time
	UseCount        int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// FactFilter narrows Facts. Zero values mean "no restriction".
type FactFilter struct {
	Status   []string
	Core     *bool
	Category string
	Limit    int
	Order    string // core | recent | oldest
}

// FactCategories are the values the tools and endpoints accept.
var FactCategories = []string{"preference", "person", "circumstance", "goal", "work", "health", "general"}

func ValidFactCategory(category string) bool {
	for _, c := range FactCategories {
		if c == category {
			return true
		}
	}
	return false
}

const factCols = `id, text, category, core, status, source_kind, source_message_id, superseded_by,
	valid_from, valid_to, last_used_at, use_count, created_at, updated_at`

func scanFact(row pgx.Row) (Fact, error) {
	var f Fact
	err := row.Scan(&f.ID, &f.Text, &f.Category, &f.Core, &f.Status, &f.SourceKind, &f.SourceMessageID,
		&f.SupersededBy, &f.ValidFrom, &f.ValidTo, &f.LastUsedAt, &f.UseCount, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Fact{}, ErrNotFound
	}
	return f, err
}

// AddFact inserts a fact. valid_from defaults to today when unset.
func (s *Store) AddFact(ctx context.Context, f Fact) (Fact, error) {
	if f.ValidFrom.IsZero() {
		f.ValidFrom = time.Now()
	}
	if f.Category == "" {
		f.Category = "general"
	}
	if f.SourceKind == "" {
		f.SourceKind = "user"
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO facts (text, category, core, status, source_kind, source_message_id, valid_from)
		VALUES ($1, $2, $3, COALESCE(NULLIF($4, ''), 'active'), $5, $6, $7)
		RETURNING `+factCols,
		strings.TrimSpace(f.Text), f.Category, f.Core, f.Status, f.SourceKind, f.SourceMessageID, Day(f.ValidFrom))
	return scanFact(row)
}

func (s *Store) GetFact(ctx context.Context, id int64) (Fact, error) {
	return scanFact(s.pool.QueryRow(ctx, `SELECT `+factCols+` FROM facts WHERE id = $1`, id))
}

// Facts lists facts by status, core flag, category and order.
func (s *Store) Facts(ctx context.Context, f FactFilter) ([]Fact, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if len(f.Status) > 0 {
		add("status = ANY($%d)", f.Status)
	}
	if f.Core != nil {
		add("core = $%d", *f.Core)
	}
	if f.Category != "" {
		add("category = $%d", f.Category)
	}
	q := `SELECT ` + factCols + ` FROM facts`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	switch f.Order {
	case "recent":
		q += ` ORDER BY created_at DESC, id DESC`
	case "oldest":
		q += ` ORDER BY valid_from, id`
	default: // core first, then most recently used
		q += ` ORDER BY core DESC, COALESCE(last_used_at, created_at) DESC, id`
	}
	if f.Limit > 0 {
		args = append(args, f.Limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		fact, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, fact)
	}
	return out, rows.Err()
}

// UpdateFact writes the editable fields (text, category, core, status,
// validity) and bumps updated_at. It never removes a row.
func (s *Store) UpdateFact(ctx context.Context, f Fact) (Fact, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE facts SET text = $2, category = $3, core = $4, status = $5,
			valid_from = $6, valid_to = $7, superseded_by = $8, updated_at = now()
		WHERE id = $1
		RETURNING `+factCols,
		f.ID, strings.TrimSpace(f.Text), f.Category, f.Core, f.Status, Day(f.ValidFrom), f.ValidTo, f.SupersededBy)
	return scanFact(row)
}

// Supersede closes oldID the day before newID becomes valid and links the two.
func (s *Store) Supersede(ctx context.Context, oldID, newID int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE facts SET status = 'superseded', superseded_by = $2, valid_to = (
			SELECT GREATEST($3::date - 1, valid_from) FROM facts WHERE id = $2
		), updated_at = now()
		WHERE id = $1 AND status = 'active'`,
		oldID, newID, Day(time.Now()))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SupersedeOn is Supersede with an explicit effective date (the day the new
// fact starts being true).
func (s *Store) SupersedeOn(ctx context.Context, oldID, newID int64, from time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE facts SET status = 'superseded', superseded_by = $2,
			valid_to = GREATEST($3::date - 1, valid_from), updated_at = now()
		WHERE id = $1 AND status = 'active'`,
		oldID, newID, Day(from))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ForgetFact marks a fact forgotten (no hard delete). ForgetMessage hides the
// source message from search, context, extraction and embeddings.
func (s *Store) ForgetFact(ctx context.Context, id int64, on time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE facts SET status = 'forgotten', valid_to = COALESCE(valid_to, $2), updated_at = now()
		WHERE id = $1 AND status <> 'forgotten'`, id, Day(on))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ForgetMessage(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE messages SET forgotten_at = now() WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ForgetDay hides every non-forgotten message the user sent or received on a
// logical day (the daily threads' day, or the local date of other messages).
func (s *Store) ForgetDay(ctx context.Context, day time.Time, from, to time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE messages m SET forgotten_at = now()
		FROM conversations c
		WHERE m.conversation_id = c.id AND m.forgotten_at IS NULL
		  AND ((c.day = $1) OR (c.day IS NULL AND m.created_at >= $2 AND m.created_at < $3))`,
		Day(day), from, to)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// TouchFacts counts facts that were put in front of the model.
func (s *Store) TouchFacts(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE facts SET use_count = use_count + 1, last_used_at = now() WHERE id = ANY($1)`, ids)
	return err
}

// CoreFacts returns active core facts, newest-used first, capped by the caller's
// prompt budget.
func (s *Store) CoreFacts(ctx context.Context, limit int) ([]Fact, error) {
	core := true
	return s.Facts(ctx, FactFilter{Status: []string{"active"}, Core: &core, Limit: limit})
}

// --- search ---------------------------------------------------------------

// MessageQuery is a full-text search over all conversations.
type MessageQuery struct {
	Text  string
	From  *time.Time // inclusive local day
	To    *time.Time // inclusive local day
	TZ    string     // zone for messages without a daily thread; default UTC
	Limit int
	// Vector makes the search hybrid (RRF of full-text and cosine). Empty
	// means keyword-only, which is the fallback when Ollama is not running.
	Vector         []float32
	EmbeddingModel string
	// Exclude lists message ids already in the prompt.
	Exclude []int64
	// Forgotten messages are never returned.
}

// MessageHit is one search result with enough context to be quoted.
type MessageHit struct {
	ID             int64
	ConversationID string
	CharacterID    string
	Day            *time.Time // daily thread day
	Role           string
	Content        string
	CreatedAt      time.Time
	Rank           float64
}

// SearchMessages finds messages by words, ranked by full-text rank with a
// recency boost, newest first for equal rank. 'simple' config: no stemming, so
// mixed Indonesian/English works without pretending one language.
// SearchMessages is keyword-only, or hybrid when q.Vector is set.
func (s *Store) SearchMessages(ctx context.Context, q MessageQuery) ([]MessageHit, error) {
	if len(q.Vector) == 0 || q.EmbeddingModel == "" {
		return s.searchMessagesKeyword(ctx, q)
	}
	wide := q
	wide.Limit = 50
	keyword, err := s.searchMessagesKeyword(ctx, wide)
	if err != nil {
		return nil, err
	}
	vector, err := s.searchMessagesVector(ctx, wide)
	if err != nil {
		return nil, err
	}
	return mergeRRF(keyword, vector, func(h MessageHit) int64 { return h.ID }, q.Limit), nil
}

func (s *Store) searchMessagesKeyword(ctx context.Context, q MessageQuery) ([]MessageHit, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, nil
	}
	args := []any{text}
	limit := q.Limit
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	where := `m.forgotten_at IS NULL AND m.search @@ websearch_to_tsquery('simple', $1)`
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
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT m.id, m.conversation_id, c.character_id, c.day, m.role, m.content, m.created_at,
		       ts_rank(m.search, websearch_to_tsquery('simple', $1))::float8 AS score
		FROM messages m JOIN conversations c ON c.id = m.conversation_id
		WHERE %s
		ORDER BY ts_rank(m.search, websearch_to_tsquery('simple', $1)) + 0.15 / (1 + EXTRACT(EPOCH FROM (now() - m.created_at)) / 86400) DESC, m.id DESC
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

// FactQuery is a full-text search over facts, optionally over a period (facts
// that were valid at any point in range, superseded included).
type FactQuery struct {
	Text  string
	From  *time.Time
	To    *time.Time
	Limit int
	// Vector and EmbeddingModel turn on hybrid search (M-10).
	Vector         []float32
	EmbeddingModel string
}

// SearchFacts is keyword-only, or hybrid when q.Vector is set.
func (s *Store) SearchFacts(ctx context.Context, q FactQuery) ([]Fact, error) {
	if len(q.Vector) == 0 || q.EmbeddingModel == "" {
		return s.searchFactsKeyword(ctx, q)
	}
	wide := q
	wide.Limit = 50
	keyword, err := s.searchFactsKeyword(ctx, wide)
	if err != nil {
		return nil, err
	}
	vector, err := s.searchFactsVector(ctx, wide)
	if err != nil {
		return nil, err
	}
	return mergeRRF(keyword, vector, func(f Fact) int64 { return f.ID }, q.Limit), nil
}

func (s *Store) searchFactsKeyword(ctx context.Context, q FactQuery) ([]Fact, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, nil
	}
	args := []any{text}
	limit := q.Limit
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	where := `status <> 'forgotten' AND search @@ websearch_to_tsquery('simple', $1)`
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
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM facts
		WHERE %s
		ORDER BY ts_rank(search, websearch_to_tsquery('simple', $1)) DESC, valid_from DESC, id DESC
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

// ImportFacts inserts facts from the old memory/<char>.json files once. A fact
// whose text is already active is skipped, so restarts do not duplicate it.
// The caller decides core; the old always-in-prompt notes import as core.
func (s *Store) ImportFacts(ctx context.Context, facts []Fact) (int, error) {
	imported := 0
	for _, f := range facts {
		text := strings.TrimSpace(f.Text)
		if text == "" {
			continue
		}
		if f.ValidFrom.IsZero() {
			f.ValidFrom = time.Now()
		}
		if f.Category == "" || !ValidFactCategory(f.Category) {
			f.Category = "general"
		}
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO facts (text, category, core, source_kind, valid_from)
			SELECT $1, $2, $3, 'user', $4
			WHERE NOT EXISTS (
				SELECT 1 FROM facts WHERE lower(text) = lower($1) AND status = 'active'
			)`, text, f.Category, f.Core, Day(f.ValidFrom))
		if err != nil {
			return imported, err
		}
		imported += int(tag.RowsAffected())
	}
	return imported, nil
}

// FactOp is one validated change from the day-close job. ApplyFactOps applies
// them in one transaction; a rejected item never reaches this method.
type FactOp struct {
	Op       string // ADD | SUPERSEDE | NONE
	ID       int64  // SUPERSEDE: the active fact being replaced
	Text     string
	Category string
	Core     bool
}

// ApplyFactOps writes a day's extracted facts atomically. SUPERSEDE inserts the
// new fact and closes the old one on the day the new one starts.
func (s *Store) ApplyFactOps(ctx context.Context, day time.Time, ops []FactOp) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	applied := 0
	for _, op := range ops {
		category := op.Category
		if category == "" || !ValidFactCategory(category) {
			category = "general"
		}
		switch op.Op {
		case "ADD":
			_, err := tx.Exec(ctx, `
				INSERT INTO facts (text, category, core, source_kind, valid_from)
				VALUES ($1, $2, $3, 'extraction', $4)`,
				strings.TrimSpace(op.Text), category, op.Core, Day(day))
			if err != nil {
				return applied, err
			}
			applied++
		case "SUPERSEDE":
			var newID int64
			err := tx.QueryRow(ctx, `
				INSERT INTO facts (text, category, core, source_kind, valid_from)
				VALUES ($1, $2, $3, 'extraction', $4)
				RETURNING id`,
				strings.TrimSpace(op.Text), category, op.Core, Day(day)).Scan(&newID)
			if err != nil {
				return applied, err
			}
			tag, err := tx.Exec(ctx, `
				UPDATE facts SET status = 'superseded', superseded_by = $2,
					valid_to = GREATEST($3::date - 1, valid_from), updated_at = now()
				WHERE id = $1 AND status = 'active'`, op.ID, newID, Day(day))
			if err != nil {
				return applied, err
			}
			if tag.RowsAffected() == 0 {
				return applied, fmt.Errorf("fact %d is not active", op.ID)
			}
			applied++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return applied, err
	}
	return applied, nil
}

// DayMessage is one message of a logical day, with the character it belongs to.
type DayMessage struct {
	ID          int64
	CharacterID string
	Role        string
	Content     string
	CreatedAt   time.Time
}

// DayMessages returns the user/assistant messages of a logical day across every
// conversation (persona chats included), minus forgotten ones. Daily threads
// are selected by conversations.day; other chats by local date.
func (s *Store) DayMessages(ctx context.Context, day time.Time, from, to time.Time) ([]DayMessage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, c.character_id, m.role, m.content, m.created_at
		FROM messages m JOIN conversations c ON c.id = m.conversation_id
		WHERE m.forgotten_at IS NULL AND m.role IN ('user','assistant')
		  AND ((c.day = $1) OR (c.day IS NULL AND m.created_at >= $2 AND m.created_at < $3))
		ORDER BY m.id`, Day(day), from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayMessage
	for rows.Next() {
		var m DayMessage
		if err := rows.Scan(&m.ID, &m.CharacterID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetRunContext replaces a run's context document (rejects are recorded there).
func (s *Store) SetRunContext(ctx context.Context, id int64, contextJSON []byte) error {
	_, err := s.pool.Exec(ctx, `UPDATE runs SET context = $2 WHERE id = $1`, id, contextJSON)
	return err
}

// SetSummary stores the rolling summary and the last message id it covers.
func (s *Store) SetSummary(ctx context.Context, conversationID, summary string, upto int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE conversations SET summary = $2, summary_upto = $3, updated_at = now()
		WHERE id = $1`, conversationID, summary, upto)
	return err
}

// NotedFacts lists facts extracted since an instant (the start of today), so a
// check-in can say "I noted: …" and the user can correct it.
func (s *Store) NotedFacts(ctx context.Context, since time.Time, limit int) ([]Fact, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+factCols+` FROM facts
		WHERE status = 'active' AND source_kind = 'extraction' AND created_at >= $1
		ORDER BY id DESC LIMIT $2`, since, limit)
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

// FactHistory returns a fact's whole supersede chain, oldest first (the fact
// that was current, everything that replaced it, and its predecessor).
func (s *Store) FactHistory(ctx context.Context, id int64) ([]Fact, error) {
	cur, err := s.GetFact(ctx, id)
	if err != nil {
		return nil, err
	}
	// Walk back to the oldest fact in the chain.
	for i := 0; i < 50; i++ {
		var prevID int64
		err := s.pool.QueryRow(ctx, `SELECT id FROM facts WHERE superseded_by = $1 LIMIT 1`, cur.ID).Scan(&prevID)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		if cur, err = s.GetFact(ctx, prevID); err != nil {
			return nil, err
		}
	}
	// Then forward from there.
	var out []Fact
	seen := map[int64]bool{}
	for i := 0; i < 50 && !seen[cur.ID]; i++ {
		seen[cur.ID] = true
		out = append(out, cur)
		if cur.SupersededBy == nil {
			break
		}
		if cur, err = s.GetFact(ctx, *cur.SupersededBy); err != nil {
			return out, err
		}
	}
	return out, nil
}
