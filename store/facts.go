package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// UserSpeaker is the sentinel speaker (and transcript owner) for the user.
const UserSpeaker = "me"

// Fact is one durable thing about the user. Facts are never overwritten:
// superseding closes valid_to on the old row and links it to the new one, so a
// period question still finds it. Forget() keeps the row, marked forgotten.
type Fact struct {
	ID              int64
	CharacterID     string // the agent that was told; facts never cross agents
	Text            string
	Category        string // preference | person | circumstance | goal | work | health | general
	Core            bool
	Status          string // active | superseded | forgotten
	SourceKind      string // user | tool | extraction
	SourceMessageID *int64
	// About is "me" or a character id: who the fact is about.
	About string
	// ToldBy is the character that said it ("" when it came from the user).
	ToldBy       string
	SupersededBy *int64
	ValidFrom    time.Time // date
	ValidTo      *time.Time
	LastUsedAt   *time.Time
	UseCount     int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Provenance says who a fact is about and who said it, when that is not the
// user speaking about themselves. Empty when the fact is plain user knowledge.
func (f Fact) Provenance() string {
	var parts []string
	if f.About != "" && f.About != UserSpeaker {
		parts = append(parts, "about "+f.About)
	}
	if f.ToldBy != "" {
		parts = append(parts, "heard from "+f.ToldBy)
	}
	return strings.Join(parts, ", ")
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

const factCols = `id, character_id, text, category, core, status, source_kind, source_message_id, superseded_by,
	valid_from, valid_to, last_used_at, use_count, created_at, updated_at, about, COALESCE(told_by, '')`

func scanFact(row pgx.Row) (Fact, error) {
	var f Fact
	err := row.Scan(&f.ID, &f.CharacterID, &f.Text, &f.Category, &f.Core, &f.Status, &f.SourceKind, &f.SourceMessageID,
		&f.SupersededBy, &f.ValidFrom, &f.ValidTo, &f.LastUsedAt, &f.UseCount, &f.CreatedAt, &f.UpdatedAt,
		&f.About, &f.ToldBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Fact{}, ErrNotFound
	}
	return f, err
}

// AddFact inserts a fact for one character. valid_from defaults to today when
// unset.
func (s *Store) AddFact(ctx context.Context, f Fact) (Fact, error) {
	if err := requireOwner(f.CharacterID); err != nil {
		return Fact{}, err
	}
	if f.ValidFrom.IsZero() {
		f.ValidFrom = time.Now()
	}
	if f.Category == "" {
		f.Category = "general"
	}
	if f.SourceKind == "" {
		f.SourceKind = "user"
	}
	if f.About == "" {
		f.About = "me"
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO facts (character_id, text, category, core, status, source_kind, source_message_id, valid_from, about, told_by)
		VALUES ($1, $2, $3, $4, COALESCE(NULLIF($5, ''), 'active'), $6, $7, $8, $9, $10)
		RETURNING `+factCols,
		f.CharacterID, strings.TrimSpace(f.Text), f.Category, f.Core, f.Status, f.SourceKind, f.SourceMessageID, Day(f.ValidFrom),
		f.About, nullIfEmpty(f.ToldBy))
	return scanFact(row)
}

// GetFact reads one character's fact; another character's id is not found.
func (s *Store) GetFact(ctx context.Context, characterID string, id int64) (Fact, error) {
	if err := requireOwner(characterID); err != nil {
		return Fact{}, err
	}
	return scanFact(s.pool.QueryRow(ctx, `SELECT `+factCols+` FROM facts WHERE id = $1 AND character_id = $2`, id, characterID))
}

// Facts lists one character's facts by status, core flag, category and order.
func (s *Store) Facts(ctx context.Context, characterID string, f FactFilter) ([]Fact, error) {
	if err := requireOwner(characterID); err != nil {
		return nil, err
	}
	args := []any{characterID}
	where := []string{"character_id = $1"}
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
	q += " WHERE " + strings.Join(where, " AND ")
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
// validity) of one character's fact and bumps updated_at. It never removes a
// row and never moves a fact to another owner.
func (s *Store) UpdateFact(ctx context.Context, f Fact) (Fact, error) {
	if err := requireOwner(f.CharacterID); err != nil {
		return Fact{}, err
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE facts SET
			embedding = CASE WHEN text IS DISTINCT FROM $2 THEN NULL ELSE embedding END,
			embedding_model = CASE WHEN text IS DISTINCT FROM $2 THEN NULL ELSE embedding_model END,
			text = $2, category = $3, core = $4, status = $5,
			valid_from = $6, valid_to = $7, superseded_by = $8, updated_at = now()
		WHERE id = $1 AND character_id = $9
		RETURNING `+factCols,
		f.ID, strings.TrimSpace(f.Text), f.Category, f.Core, f.Status, Day(f.ValidFrom), f.ValidTo, f.SupersededBy, f.CharacterID)
	return scanFact(row)
}

// Supersede closes oldID the day before newID becomes valid and links the two.
// Both must belong to the same character.
func (s *Store) Supersede(ctx context.Context, characterID string, oldID, newID int64) error {
	return s.supersedeOn(ctx, characterID, oldID, newID, Day(time.Now()))
}

// SupersedeOn is Supersede with an explicit effective date (the day the new
// fact starts being true).
func (s *Store) SupersedeOn(ctx context.Context, characterID string, oldID, newID int64, from time.Time) error {
	return s.supersedeOn(ctx, characterID, oldID, newID, Day(from))
}

func (s *Store) supersedeOn(ctx context.Context, characterID string, oldID, newID int64, from time.Time) error {
	if err := requireOwner(characterID); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE facts SET status = 'superseded', superseded_by = $2,
			valid_to = GREATEST($3::date - 1, valid_from), updated_at = now()
		WHERE id = $1 AND character_id = $4 AND status = 'active'
		  AND EXISTS (SELECT 1 FROM facts WHERE id = $2 AND character_id = $4)`,
		oldID, newID, from, characterID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ForgetFact marks one character's fact forgotten (no hard delete).
// ForgetMessage hides the source message from search, context, extraction and
// embeddings.
func (s *Store) ForgetFact(ctx context.Context, characterID string, id int64, on time.Time) error {
	if err := requireOwner(characterID); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE facts SET status = 'forgotten', valid_to = COALESCE(valid_to, $3), updated_at = now()
		WHERE id = $1 AND character_id = $2 AND status <> 'forgotten'`, id, characterID, Day(on))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ForgetMessage hides one message of a character's own conversation.
func (s *Store) ForgetMessage(ctx context.Context, characterID string, id int64) error {
	if err := requireOwner(characterID); err != nil {
		return err
	}
	var convID string
	err := s.pool.QueryRow(ctx, `
		UPDATE messages m SET forgotten_at = now()
		FROM conversations c
		WHERE m.conversation_id = c.id AND m.id = $1 AND c.character_id = $2
		RETURNING m.conversation_id`, id, characterID).Scan(&convID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// A stored summary may quote the message; drop it so it cannot come back
	// through the next turn. It is regenerated from the remaining messages.
	_, err = s.pool.Exec(ctx, `
		UPDATE conversations SET summary = NULL, summary_upto = NULL
		WHERE id = $1`, convID)
	return err
}

// ForgetDay hides every non-forgotten message one character sent or received on
// a logical day (the daily threads' day, or the local date of other messages).
func (s *Store) ForgetDay(ctx context.Context, characterID string, day time.Time, from, to time.Time) (int64, error) {
	if err := requireOwner(characterID); err != nil {
		return 0, err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE messages m SET forgotten_at = now()
		FROM conversations c
		WHERE m.conversation_id = c.id AND m.forgotten_at IS NULL AND c.character_id = $3
		  AND ((c.day = $1) OR (c.day IS NULL AND m.created_at >= $2 AND m.created_at < $4))`,
		Day(day), from, characterID, to)
	if err != nil {
		return 0, err
	}
	// Drop summaries that may quote a message from this day; the next turn
	// regenerates them from what is left.
	_, err = s.pool.Exec(ctx, `
		UPDATE conversations c SET summary = NULL, summary_upto = NULL
		WHERE c.character_id = $3 AND ((c.day = $1) OR (c.day IS NULL AND EXISTS (
			SELECT 1 FROM messages m
			WHERE m.conversation_id = c.id AND m.created_at >= $2 AND m.created_at < $4)))`,
		Day(day), from, characterID, to)
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

// CoreFacts returns one character's active core facts, newest-used first,
// capped by the caller's prompt budget.
func (s *Store) CoreFacts(ctx context.Context, characterID string, limit int) ([]Fact, error) {
	core := true
	return s.Facts(ctx, characterID, FactFilter{Status: []string{"active"}, Core: &core, Limit: limit})
}

// --- search ---------------------------------------------------------------

// MessageQuery is a full-text search inside one character's conversations.
// CharacterID is required: there is no search across agents.
type MessageQuery struct {
	CharacterID string
	Text        string
	From        *time.Time // inclusive local day
	To          *time.Time // inclusive local day
	TZ          string     // zone for messages without a daily thread; default UTC
	Limit       int
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
	if err := requireOwner(q.CharacterID); err != nil {
		return nil, err
	}
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
	args := []any{text, q.CharacterID}
	limit := q.Limit
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	where := `m.forgotten_at IS NULL AND c.character_id = $2 AND m.role IN ('user','assistant') AND m.search @@ websearch_to_tsquery('simple', $1)`
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

// FactQuery is a full-text search inside one character's facts, optionally over
// a period (facts that were valid at any point in range, superseded included).
// CharacterID is required: there is no search across agents.
type FactQuery struct {
	CharacterID string
	Text        string
	From        *time.Time
	To          *time.Time
	Limit       int
	// Vector and EmbeddingModel turn on hybrid search (M-10).
	Vector         []float32
	EmbeddingModel string
}

// SearchFacts is keyword-only, or hybrid when q.Vector is set.
func (s *Store) SearchFacts(ctx context.Context, q FactQuery) ([]Fact, error) {
	if err := requireOwner(q.CharacterID); err != nil {
		return nil, err
	}
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
	args := []any{text, q.CharacterID}
	limit := q.Limit
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	where := `character_id = $2 AND status <> 'forgotten' AND search @@ websearch_to_tsquery('simple', $1)`
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

// ImportFacts inserts a character's facts from the old memory/<char>.json files
// once. A fact whose text is already active for that same character is skipped,
// so restarts do not duplicate it. The caller decides core; the old
// always-in-prompt notes import as core.
func (s *Store) ImportFacts(ctx context.Context, characterID string, facts []Fact) (int, error) {
	if err := requireOwner(characterID); err != nil {
		return 0, err
	}
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
			INSERT INTO facts (character_id, text, category, core, source_kind, valid_from)
			SELECT $1, $2, $3, $4, 'user', $5
			WHERE NOT EXISTS (
				SELECT 1 FROM facts WHERE character_id = $1 AND lower(text) = lower($2)
			)`, characterID, text, f.Category, f.Core, Day(f.ValidFrom))
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
	Op              string // ADD | SUPERSEDE | NONE
	ID              int64  // SUPERSEDE: the active fact being replaced
	Text            string
	Category        string
	Core            bool
	About           string // "" means me
	ToldBy          string // set by the server from the source message's speaker
	SourceMessageID *int64 // the message the day-close job extracted it from
}

// ApplyFactOps writes one character's day of extracted facts atomically.
// SUPERSEDE inserts the new fact and closes the old one on the day the new one
// starts. The SQL repeats the owner check: a cross-owner supersede can never
// take effect even if validation is bypassed.
func (s *Store) ApplyFactOps(ctx context.Context, characterID string, day time.Time, ops []FactOp) (int, error) {
	if err := requireOwner(characterID); err != nil {
		return 0, err
	}
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
				INSERT INTO facts (character_id, text, category, core, source_kind, valid_from, source_message_id, about, told_by)
				VALUES ($1, $2, $3, $4, 'extraction', $5, $6, COALESCE(NULLIF($7, ''), 'me'), $8)`,
				characterID, strings.TrimSpace(op.Text), category, op.Core, Day(day), op.SourceMessageID,
				op.About, nullIfEmpty(op.ToldBy))
			if err != nil {
				return applied, err
			}
			applied++
		case "SUPERSEDE":
			var newID int64
			err := tx.QueryRow(ctx, `
				INSERT INTO facts (character_id, text, category, core, source_kind, valid_from, source_message_id, about, told_by)
				VALUES ($1, $2, $3, $4, 'extraction', $5, $6, COALESCE(NULLIF($7, ''), 'me'), $8)
				RETURNING id`,
				characterID, strings.TrimSpace(op.Text), category, op.Core, Day(day), op.SourceMessageID,
				op.About, nullIfEmpty(op.ToldBy)).Scan(&newID)
			if err != nil {
				return applied, err
			}
			tag, err := tx.Exec(ctx, `
				UPDATE facts SET status = 'superseded', superseded_by = $2,
					valid_to = GREATEST($3::date - 1, valid_from), updated_at = now()
				WHERE id = $1 AND character_id = $4 AND status = 'active'`, op.ID, newID, Day(day), characterID)
			if err != nil {
				return applied, err
			}
			if tag.RowsAffected() == 0 {
				return applied, fmt.Errorf("fact %d is not active for %s", op.ID, characterID)
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
	Speaker     string // "" when it came from the user in a 1:1 chat
	CreatedAt   time.Time
}

// DayMessages returns one character's user/assistant messages of a logical day,
// minus forgotten ones. Daily threads are selected by conversations.day; other
// chats by local date. There is no all-characters variant: the close job asks
// once per owner.
func (s *Store) DayMessages(ctx context.Context, characterID string, day time.Time, from, to time.Time) ([]DayMessage, error) {
	if err := requireOwner(characterID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, c.character_id, m.role, m.content, COALESCE(m.speaker, ''), m.created_at
		FROM messages m JOIN conversations c ON c.id = m.conversation_id
		WHERE m.forgotten_at IS NULL AND m.role IN ('user','assistant') AND c.character_id = $4
		  AND ((c.day = $1) OR (c.day IS NULL AND m.created_at >= $2 AND m.created_at < $3))
		ORDER BY m.id`, Day(day), from, to, characterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayMessage
	for rows.Next() {
		var m DayMessage
		if err := rows.Scan(&m.ID, &m.CharacterID, &m.Role, &m.Content, &m.Speaker, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DayCharacters lists the characters that said something on a logical day, so
// the close job can run once per owner (sorted, so a poll is deterministic).
func (s *Store) DayCharacters(ctx context.Context, day time.Time, from, to time.Time) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT c.character_id
		FROM messages m JOIN conversations c ON c.id = m.conversation_id
		WHERE m.forgotten_at IS NULL AND m.role IN ('user','assistant')
		  AND ((c.day = $1) OR (c.day IS NULL AND m.created_at >= $2 AND m.created_at < $3))
		ORDER BY c.character_id`, Day(day), from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
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

// NotedFacts lists one character's facts extracted since an instant (the start
// of today), so a check-in can say "I noted: …" and the user can correct it.
func (s *Store) NotedFacts(ctx context.Context, characterID string, since time.Time, limit int) ([]Fact, error) {
	if err := requireOwner(characterID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+factCols+` FROM facts
		WHERE character_id = $3 AND status = 'active' AND source_kind = 'extraction' AND created_at >= $1
		ORDER BY id DESC LIMIT $2`, since, limit, characterID)
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

// FactHistory returns one character's fact and its whole supersede chain,
// oldest first (the fact that was current, everything that replaced it, and its
// predecessor). Another character's chain is never reachable.
func (s *Store) FactHistory(ctx context.Context, characterID string, id int64) ([]Fact, error) {
	if err := requireOwner(characterID); err != nil {
		return nil, err
	}
	cur, err := s.GetFact(ctx, characterID, id)
	if err != nil {
		return nil, err
	}
	// Walk back to the oldest fact in the chain.
	for i := 0; i < 50; i++ {
		var prevID int64
		err := s.pool.QueryRow(ctx, `SELECT id FROM facts WHERE superseded_by = $1 AND character_id = $2 LIMIT 1`, cur.ID, characterID).Scan(&prevID)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		if cur, err = s.GetFact(ctx, characterID, prevID); err != nil {
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
		if cur, err = s.GetFact(ctx, characterID, *cur.SupersededBy); err != nil {
			return out, err
		}
	}
	return out, nil
}
