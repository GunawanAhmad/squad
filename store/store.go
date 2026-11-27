// Package store owns the Postgres schema and every row the app writes.
// Migrations are numbered .sql files embedded in the binary; there is no
// migration library, only the schema_version row.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned by Get* methods when the row does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrOwnerRequired is returned by every fact/message read that forgot to name
// the character it acts for. There is deliberately no "all characters" mode.
var ErrOwnerRequired = errors.New("store: a character id is required")

func requireOwner(characterID string) error {
	if strings.TrimSpace(characterID) == "" {
		return ErrOwnerRequired
	}
	return nil
}

// Store is a Postgres connection pool with the schema migrated.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects, checks the database is reachable, and applies pending
// migrations. It returns an error if Postgres is down: the app cannot run
// without it.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres unreachable (is `make search` running?): %w", err)
	}
	s := &Store{pool: pool}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// NewWithPool is for tests that already built a pool (storetest).
func NewWithPool(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Close() { s.pool.Close() }

// SchemaVersion is the highest applied migration number.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.pool.QueryRow(ctx, `SELECT version FROM schema_version`).Scan(&v)
	return v, err
}

type Conversation struct {
	ID          string
	CharacterID string
	Day         *time.Time // set for daily assistant threads
	Summary     string
	SummaryUpto *int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Message struct {
	ID             int64
	ConversationID string
	Role           string // user | assistant | tool | trigger
	Content        string
	// Speaker is who said it in a room: a character id, "me" for the user,
	// or "" in a 1:1 chat (the conversation's owner, or the user).
	Speaker     string
	ToolCalls   []byte // jsonb
	ToolCallID  string
	RunID       *int64
	CreatedAt   time.Time
	ForgottenAt *time.Time
}

type Run struct {
	ID             int64
	Kind           string
	ConversationID *string
	Model          string
	Context        []byte // jsonb
	Status         string // running | ok | error | cancelled | limited
	Error          string
	InputTokens    *int
	OutputTokens   *int
	StartedAt      time.Time
	EndedAt        *time.Time
}

type ToolCall struct {
	ID        int64
	RunID     int64
	Name      string
	Args      []byte // jsonb
	Result    string
	Undo      []byte // jsonb
	Status    string // ok | error | undone
	CreatedAt time.Time
}

func (s *Store) CreateConversation(ctx context.Context, c Conversation) (Conversation, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO conversations (id, character_id, day)
		VALUES ($1, $2, $3)
		ON CONFLICT (id) DO NOTHING
		RETURNING id, character_id, day, COALESCE(summary, ''), summary_upto, created_at, updated_at`,
		c.ID, c.CharacterID, c.Day)
	got, err := scanConversation(row)
	if errors.Is(err, ErrNotFound) { // ON CONFLICT DO NOTHING returned no row
		return s.GetConversation(ctx, c.ID)
	}
	return got, err
}

func (s *Store) GetConversation(ctx context.Context, id string) (Conversation, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, character_id, day, COALESCE(summary, ''), summary_upto, created_at, updated_at
		FROM conversations WHERE id = $1`, id)
	return scanConversation(row)
}

// ListConversations returns a character's threads, newest activity first.
func (s *Store) ListConversations(ctx context.Context, characterID string, limit int) ([]Conversation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, character_id, day, COALESCE(summary, ''), summary_upto, created_at, updated_at
		FROM conversations WHERE character_id = $1
		ORDER BY updated_at DESC, id DESC
		LIMIT $2`, characterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanConversation(row pgx.Row) (Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.CharacterID, &c.Day, &c.Summary, &c.SummaryUpto, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	return c, err
}

func (s *Store) AddMessage(ctx context.Context, m Message) (Message, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO messages (conversation_id, role, content, speaker, tool_calls, tool_call_id, run_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at`,
		m.ConversationID, m.Role, m.Content, nullIfEmpty(m.Speaker), m.ToolCalls, nullIfEmpty(m.ToolCallID), m.RunID)
	err := row.Scan(&m.ID, &m.CreatedAt)
	if err != nil {
		return Message{}, err
	}
	_, err = s.pool.Exec(ctx, `UPDATE conversations SET updated_at = now() WHERE id = $1`, m.ConversationID)
	return m, err
}

// Messages returns messages oldest first. With after=0 and a positive limit it
// returns the newest page (so a long chat opens at the present, not the past);
// with after>0 it returns the next page after that id. limit <= 0 means all.
func (s *Store) Messages(ctx context.Context, conversationID string, after int64, limit int) ([]Message, error) {
	const cols = `id, conversation_id, role, content, COALESCE(speaker, ''), COALESCE(tool_calls, 'null'::jsonb),
		COALESCE(tool_call_id, ''), run_id, created_at, forgotten_at`

	var rows pgx.Rows
	var err error
	if after == 0 && limit > 0 {
		rows, err = s.pool.Query(ctx, `
			SELECT * FROM (
				SELECT id, conversation_id, role, content, COALESCE(speaker, '') AS speaker,
				       COALESCE(tool_calls, 'null'::jsonb) AS tool_calls,
				       COALESCE(tool_call_id, '') AS tool_call_id,
				       run_id, created_at, forgotten_at
				FROM messages WHERE conversation_id = $1 ORDER BY id DESC LIMIT $2
			) t ORDER BY id`, conversationID, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT `+cols+` FROM messages
			WHERE conversation_id = $1 AND id > $2
			ORDER BY id
			LIMIT CASE WHEN $3 > 0 THEN $3 END`, conversationID, after, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// AddMessageAt inserts a message with an explicit created_at. Used by seeds
// and tests; the app itself always uses AddMessage.
func (s *Store) AddMessageAt(ctx context.Context, m Message, at time.Time) (Message, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO messages (conversation_id, role, content, speaker, tool_calls, tool_call_id, run_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`,
		m.ConversationID, m.Role, m.Content, nullIfEmpty(m.Speaker), m.ToolCalls, nullIfEmpty(m.ToolCallID), m.RunID, at)
	if err := row.Scan(&m.ID, &m.CreatedAt); err != nil {
		return Message{}, err
	}
	_, err := s.pool.Exec(ctx, `UPDATE conversations SET updated_at = now() WHERE id = $1`, m.ConversationID)
	return m, err
}

// GetMessage returns one of a character's messages (to show where a fact came
// from, or to hide it when the fact is forgotten).
func (s *Store) GetMessage(ctx context.Context, characterID string, id int64) (Message, error) {
	if err := requireOwner(characterID); err != nil {
		return Message{}, err
	}
	var m Message
	err := s.pool.QueryRow(ctx, `
		SELECT m.id, m.conversation_id, m.role, m.content, COALESCE(m.speaker, ''), COALESCE(m.tool_calls, 'null'::jsonb),
		       COALESCE(m.tool_call_id, ''), m.run_id, m.created_at, m.forgotten_at
		FROM messages m JOIN conversations c ON c.id = m.conversation_id
		WHERE m.id = $1 AND c.character_id = $2`, id, characterID).
		Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.Speaker, &m.ToolCalls,
			&m.ToolCallID, &m.RunID, &m.CreatedAt, &m.ForgottenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	return m, err
}

// RecentMessages returns the last n messages, oldest first.
func (s *Store) RecentMessages(ctx context.Context, conversationID string, n int) ([]Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT * FROM (
			SELECT id, conversation_id, role, content, COALESCE(speaker, ''), COALESCE(tool_calls, 'null'::jsonb),
			       COALESCE(tool_call_id, ''), run_id, created_at, forgotten_at
			FROM messages WHERE conversation_id = $1 AND forgotten_at IS NULL
			ORDER BY id DESC LIMIT $2
		) t ORDER BY id`, conversationID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func scanMessages(rows pgx.Rows) ([]Message, error) {
	var out []Message
	for rows.Next() {
		var m Message
		err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.Speaker, &m.ToolCalls,
			&m.ToolCallID, &m.RunID, &m.CreatedAt, &m.ForgottenAt)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) StartRun(ctx context.Context, r Run) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO runs (kind, conversation_id, model, context, status)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		r.Kind, r.ConversationID, r.Model, r.Context, r.Status).Scan(&id)
	return id, err
}

func (s *Store) FinishRun(ctx context.Context, id int64, status, errMsg string, inputTokens, outputTokens *int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE runs SET status = $2, error = $3, input_tokens = $4, output_tokens = $5, ended_at = now()
		WHERE id = $1`, id, status, nullIfEmpty(errMsg), inputTokens, outputTokens)
	return err
}

func (s *Store) AddToolCall(ctx context.Context, tc ToolCall) (ToolCall, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO tool_calls (run_id, name, args, result, undo, status)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`,
		tc.RunID, tc.Name, tc.Args, tc.Result, tc.Undo, tc.Status).Scan(&tc.ID, &tc.CreatedAt)
	return tc, err
}

// Runs returns a conversation's runs, oldest first (for traces and tests).
func (s *Store) Runs(ctx context.Context, conversationID string) ([]Run, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, kind, conversation_id, model, context, status, COALESCE(error, ''),
		       input_tokens, output_tokens, started_at, ended_at
		FROM runs WHERE conversation_id = $1 ORDER BY id`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Run
	for rows.Next() {
		var r Run
		err := rows.Scan(&r.ID, &r.Kind, &r.ConversationID, &r.Model, &r.Context, &r.Status,
			&r.Error, &r.InputTokens, &r.OutputTokens, &r.StartedAt, &r.EndedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ToolCalls returns the tool calls made by one run, oldest first.
func (s *Store) ToolCalls(ctx context.Context, runID int64) ([]ToolCall, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id, name, args, COALESCE(result, ''), COALESCE(undo, 'null'::jsonb), status, created_at
		FROM tool_calls WHERE run_id = $1 ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ToolCall
	for rows.Next() {
		var t ToolCall
		err := rows.Scan(&t.ID, &t.RunID, &t.Name, &t.Args, &t.Result, &t.Undo, &t.Status, &t.CreatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// RecentMessagesAfter returns the newest n messages with id > after, oldest
// first. Used once a rolling summary covers the older part of a thread.
func (s *Store) RecentMessagesAfter(ctx context.Context, conversationID string, after int64, n int) ([]Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT * FROM (
			SELECT id, conversation_id, role, content, COALESCE(speaker, ''), COALESCE(tool_calls, 'null'::jsonb),
			       COALESCE(tool_call_id, ''), run_id, created_at, forgotten_at
			FROM messages WHERE conversation_id = $1 AND id > $2 AND forgotten_at IS NULL
			ORDER BY id DESC LIMIT $3
		) t ORDER BY id`, conversationID, after, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// RunsByKind returns runs of one kind (close jobs have no conversation), for
// traces and tests.
func (s *Store) RunsByKind(ctx context.Context, kind string) ([]Run, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, kind, conversation_id, model, context, status, COALESCE(error, ''),
		       input_tokens, output_tokens, started_at, ended_at
		FROM runs WHERE kind = $1 ORDER BY id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.Kind, &r.ConversationID, &r.Model, &r.Context, &r.Status,
			&r.Error, &r.InputTokens, &r.OutputTokens, &r.StartedAt, &r.EndedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
