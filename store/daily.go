package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Day returns t's calendar date at midnight UTC. Date columns carry no zone:
// the assistant decides which calendar day a moment belongs to (04:00 boundary,
// settings.tz) and hands the store a date.
func Day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func DayString(t time.Time) string { return t.Format("2006-01-02") }

type Project struct {
	ID        int64
	Name      string
	CreatedAt time.Time
}

type Task struct {
	ID              int64
	Title           string
	Notes           string
	ProjectID       *int64
	Status          string // open | done | dropped
	PlannedOn       *time.Time
	DueOn           *time.Time
	TimesMoved      int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     *time.Time
	SourceMessageID *int64
	ToolCallID      *int64
}

type Activity struct {
	ID              int64
	Day             time.Time
	Description     string
	Minutes         *int
	ProjectID       *int64
	TaskID          *int64
	SourceMessageID *int64
	ToolCallID      *int64
	CreatedAt       time.Time
}

type Reminder struct {
	ID              int64
	Text            string
	RemindAt        time.Time
	Repeat          string // "" (once) | daily | weekdays
	Status          string // pending | fired | cancelled
	FiredAt         *time.Time
	SourceMessageID *int64
	ToolCallID      *int64
	CreatedAt       time.Time
}

type Checkin struct {
	Day         time.Time
	Kind        string
	CharacterID string
	Status      string // pending | sent | failed | skipped
	Attempts    int
	MessageID   *int64
	RunID       *int64
	UpdatedAt   time.Time
}

// TaskFilter narrows Tasks. Zero values mean "no restriction".
type TaskFilter struct {
	Status        []string
	ProjectID     *int64
	PlannedOn     *time.Time // planned_on = day
	PlannedBefore *time.Time // planned_on < day
	PlannedFrom   *time.Time // planned_on >= day
	PlannedTo     *time.Time // planned_on <= day
	CompletedFrom *time.Time // completed_at >= instant
	CompletedTo   *time.Time // completed_at < instant
	Limit         int
	Order         string // planned | moved | created | newest
}

type ActivityFilter struct {
	From      *time.Time // day >=
	To        *time.Time // day <=
	ProjectID *int64
	Limit     int
}

type ReminderFilter struct {
	Status    []string
	DueBefore *time.Time // remind_at <=
	From      *time.Time // remind_at >=
	To        *time.Time // remind_at <
	Limit     int
}

func (s *Store) EnsureProject(ctx context.Context, name string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, errors.New("project name is required")
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO projects (name) VALUES ($1)
		ON CONFLICT (lower(name)) DO UPDATE SET name = projects.name
		RETURNING id, name, created_at`, name)
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.CreatedAt)
	return p, err
}

func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, created_at FROM projects ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const taskCols = `id, title, COALESCE(notes, ''), project_id, status, planned_on, due_on, times_moved,
	created_at, updated_at, completed_at, source_message_id, tool_call_id`

func scanTask(row pgx.Row) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.Title, &t.Notes, &t.ProjectID, &t.Status, &t.PlannedOn, &t.DueOn,
		&t.TimesMoved, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt, &t.SourceMessageID, &t.ToolCallID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

func (s *Store) CreateTask(ctx context.Context, t Task) (Task, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO tasks (title, notes, project_id, status, planned_on, due_on, times_moved,
		                   completed_at, source_message_id, tool_call_id)
		VALUES ($1, $2, $3, COALESCE(NULLIF($4, ''), 'open'), $5, $6, $7, $8, $9, $10)
		RETURNING `+taskCols,
		t.Title, nullIfEmpty(t.Notes), t.ProjectID, t.Status, t.PlannedOn, t.DueOn, t.TimesMoved,
		t.CompletedAt, t.SourceMessageID, t.ToolCallID)
	return scanTask(row)
}

func (s *Store) GetTask(ctx context.Context, id int64) (Task, error) {
	return scanTask(s.pool.QueryRow(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = $1`, id))
}

// UpdateTask writes the whole mutable row and bumps updated_at; undo uses it to
// restore a previous state verbatim.
func (s *Store) UpdateTask(ctx context.Context, t Task) (Task, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE tasks SET title = $2, notes = $3, project_id = $4, status = $5, planned_on = $6,
			due_on = $7, times_moved = $8, completed_at = $9, updated_at = now()
		WHERE id = $1
		RETURNING `+taskCols,
		t.ID, t.Title, nullIfEmpty(t.Notes), t.ProjectID, t.Status, t.PlannedOn, t.DueOn,
		t.TimesMoved, t.CompletedAt)
	return scanTask(row)
}

func (s *Store) DeleteTask(ctx context.Context, id int64) error {
	return s.deleteOne(ctx, "tasks", id)
}

func (s *Store) Tasks(ctx context.Context, f TaskFilter) ([]Task, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if len(f.Status) > 0 {
		add("status = ANY($%d)", f.Status)
	}
	if f.ProjectID != nil {
		add("project_id = $%d", *f.ProjectID)
	}
	if f.PlannedOn != nil {
		add("planned_on = $%d", Day(*f.PlannedOn))
	}
	if f.PlannedBefore != nil {
		add("planned_on < $%d", Day(*f.PlannedBefore))
	}
	if f.PlannedFrom != nil {
		add("planned_on >= $%d", Day(*f.PlannedFrom))
	}
	if f.PlannedTo != nil {
		add("planned_on <= $%d", Day(*f.PlannedTo))
	}
	if f.CompletedFrom != nil {
		add("completed_at >= $%d", *f.CompletedFrom)
	}
	if f.CompletedTo != nil {
		add("completed_at < $%d", *f.CompletedTo)
	}

	q := `SELECT ` + taskCols + ` FROM tasks`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	switch f.Order {
	case "moved":
		q += ` ORDER BY planned_on NULLS LAST, times_moved DESC, id`
	case "created":
		q += ` ORDER BY created_at, id`
	case "newest":
		q += ` ORDER BY id DESC`
	default:
		q += ` ORDER BY planned_on NULLS LAST, id`
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

	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

const activityCols = `id, day, description, minutes, project_id, task_id, source_message_id, tool_call_id, created_at`

func scanActivity(row pgx.Row) (Activity, error) {
	var a Activity
	err := row.Scan(&a.ID, &a.Day, &a.Description, &a.Minutes, &a.ProjectID, &a.TaskID,
		&a.SourceMessageID, &a.ToolCallID, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Activity{}, ErrNotFound
	}
	return a, err
}

func (s *Store) AddActivity(ctx context.Context, a Activity) (Activity, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO activities (day, description, minutes, project_id, task_id, source_message_id, tool_call_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+activityCols,
		Day(a.Day), a.Description, a.Minutes, a.ProjectID, a.TaskID, a.SourceMessageID, a.ToolCallID)
	return scanActivity(row)
}

func (s *Store) GetActivity(ctx context.Context, id int64) (Activity, error) {
	return scanActivity(s.pool.QueryRow(ctx, `SELECT `+activityCols+` FROM activities WHERE id = $1`, id))
}

func (s *Store) UpdateActivity(ctx context.Context, a Activity) (Activity, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE activities SET day = $2, description = $3, minutes = $4, project_id = $5, task_id = $6
		WHERE id = $1
		RETURNING `+activityCols,
		a.ID, Day(a.Day), a.Description, a.Minutes, a.ProjectID, a.TaskID)
	return scanActivity(row)
}

func (s *Store) DeleteActivity(ctx context.Context, id int64) error {
	return s.deleteOne(ctx, "activities", id)
}

func (s *Store) Activities(ctx context.Context, f ActivityFilter) ([]Activity, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.From != nil {
		add("day >= $%d", Day(*f.From))
	}
	if f.To != nil {
		add("day <= $%d", Day(*f.To))
	}
	if f.ProjectID != nil {
		add("project_id = $%d", *f.ProjectID)
	}

	q := `SELECT ` + activityCols + ` FROM activities`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += ` ORDER BY day, id`
	if f.Limit > 0 {
		args = append(args, f.Limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Activity
	for rows.Next() {
		a, err := scanActivity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

const reminderCols = `id, text, remind_at, COALESCE(repeat, ''), status, fired_at,
	source_message_id, tool_call_id, created_at`

func scanReminder(row pgx.Row) (Reminder, error) {
	var r Reminder
	err := row.Scan(&r.ID, &r.Text, &r.RemindAt, &r.Repeat, &r.Status, &r.FiredAt,
		&r.SourceMessageID, &r.ToolCallID, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reminder{}, ErrNotFound
	}
	return r, err
}

func (s *Store) AddReminder(ctx context.Context, r Reminder) (Reminder, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO reminders (text, remind_at, repeat, status, source_message_id, tool_call_id)
		VALUES ($1, $2, $3, COALESCE(NULLIF($4, ''), 'pending'), $5, $6)
		RETURNING `+reminderCols,
		r.Text, r.RemindAt, nullIfEmpty(r.Repeat), r.Status, r.SourceMessageID, r.ToolCallID)
	return scanReminder(row)
}

func (s *Store) GetReminder(ctx context.Context, id int64) (Reminder, error) {
	return scanReminder(s.pool.QueryRow(ctx, `SELECT `+reminderCols+` FROM reminders WHERE id = $1`, id))
}

func (s *Store) UpdateReminder(ctx context.Context, r Reminder) (Reminder, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE reminders SET text = $2, remind_at = $3, repeat = $4, status = $5, fired_at = $6
		WHERE id = $1
		RETURNING `+reminderCols,
		r.ID, r.Text, r.RemindAt, nullIfEmpty(r.Repeat), r.Status, r.FiredAt)
	return scanReminder(row)
}

// FireReminder atomically moves a pending reminder on: next is its next firing
// time for a repeating reminder, nil for a one-off (which becomes fired). It
// reports false when another poll already fired it.
func (s *Store) FireReminder(ctx context.Context, id int64, now time.Time, next *time.Time) (bool, error) {
	status := "fired"
	if next != nil {
		status = "pending"
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE reminders SET status = $3, remind_at = COALESCE($2, remind_at), fired_at = $4
		WHERE id = $1 AND status = 'pending' AND remind_at <= $4`, id, next, status, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) DeleteReminder(ctx context.Context, id int64) error {
	return s.deleteOne(ctx, "reminders", id)
}

func (s *Store) Reminders(ctx context.Context, f ReminderFilter) ([]Reminder, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if len(f.Status) > 0 {
		add("status = ANY($%d)", f.Status)
	}
	if f.DueBefore != nil {
		add("remind_at <= $%d", *f.DueBefore)
	}
	if f.From != nil {
		add("remind_at >= $%d", *f.From)
	}
	if f.To != nil {
		add("remind_at < $%d", *f.To)
	}

	q := `SELECT ` + reminderCols + ` FROM reminders`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += ` ORDER BY remind_at, id`
	if f.Limit > 0 {
		args = append(args, f.Limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Reminder
	for rows.Next() {
		r, err := scanReminder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Checkin(ctx context.Context, day time.Time, kind, characterID string) (Checkin, error) {
	if err := requireOwner(characterID); err != nil {
		return Checkin{}, err
	}
	var c Checkin
	err := s.pool.QueryRow(ctx, `
		SELECT day, kind, character_id, status, attempts, message_id, run_id, updated_at
		FROM checkins WHERE day = $1 AND kind = $2 AND character_id = $3`, Day(day), kind, characterID).
		Scan(&c.Day, &c.Kind, &c.CharacterID, &c.Status, &c.Attempts, &c.MessageID, &c.RunID, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Checkin{}, ErrNotFound
	}
	return c, err
}

// ClaimCheckin inserts a pending row and reports whether this caller got it.
// The (day, kind, character_id) primary key makes a second poll lose the race
// instead of sending a second check-in; the close job gets exactly one claim per
// character per day.
func (s *Store) ClaimCheckin(ctx context.Context, day time.Time, kind, characterID string) (bool, error) {
	if err := requireOwner(characterID); err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO checkins (day, kind, character_id, status) VALUES ($1, $2, $3, 'pending')
		ON CONFLICT (day, kind, character_id) DO NOTHING`, Day(day), kind, characterID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) FinishCheckin(ctx context.Context, day time.Time, kind, characterID, status string, messageID, runID *int64) error {
	if err := requireOwner(characterID); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE checkins SET status = $4, message_id = $5, run_id = $6, updated_at = now()
		WHERE day = $1 AND kind = $2 AND character_id = $3`, Day(day), kind, characterID, status, messageID, runID)
	return err
}

// FailCheckin records a failed attempt and returns the new attempt count.
func (s *Store) FailCheckin(ctx context.Context, day time.Time, kind, characterID string) (int, error) {
	if err := requireOwner(characterID); err != nil {
		return 0, err
	}
	var attempts int
	err := s.pool.QueryRow(ctx, `
		UPDATE checkins SET status = 'failed', attempts = attempts + 1, updated_at = now()
		WHERE day = $1 AND kind = $2 AND character_id = $3 RETURNING attempts`, Day(day), kind, characterID).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return attempts, err
}

// SkipCheckin writes a skipped row, also downgrading a pending or failed row
// from an earlier poll. A sent check-in is never rewritten.
func (s *Store) SkipCheckin(ctx context.Context, day time.Time, kind, characterID string) error {
	if err := requireOwner(characterID); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO checkins (day, kind, character_id, status) VALUES ($1, $2, $3, 'skipped')
		ON CONFLICT (day, kind, character_id) DO UPDATE SET status = 'skipped', updated_at = now()
		WHERE checkins.status <> 'sent'`, Day(day), kind, characterID)
	return err
}

func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSettings upserts the given keys in one transaction.
func (s *Store) SetSettings(ctx context.Context, vals map[string]string) error {
	if len(vals) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for k, v := range vals {
		if _, err := tx.Exec(ctx, `
			INSERT INTO settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// StartToolCall records a tool call in flight, so the tool itself knows its id
// and can stamp the rows it writes (the Today panel's undo uses those ids).
func (s *Store) StartToolCall(ctx context.Context, tc ToolCall) (ToolCall, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO tool_calls (run_id, name, args, status)
		VALUES ($1, $2, $3, 'running') RETURNING id, created_at`,
		tc.RunID, tc.Name, tc.Args).Scan(&tc.ID, &tc.CreatedAt)
	tc.Status = "running"
	return tc, err
}

func (s *Store) FinishToolCall(ctx context.Context, id int64, result string, undo []byte, status string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE tool_calls SET result = $2, undo = $3, status = $4 WHERE id = $1`,
		id, result, undo, status)
	return err
}

func (s *Store) GetToolCall(ctx context.Context, id int64) (ToolCall, error) {
	var t ToolCall
	err := s.pool.QueryRow(ctx, `
		SELECT id, run_id, name, args, COALESCE(result, ''), COALESCE(undo, 'null'::jsonb), status, created_at
		FROM tool_calls WHERE id = $1`, id).
		Scan(&t.ID, &t.RunID, &t.Name, &t.Args, &t.Result, &t.Undo, &t.Status, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolCall{}, ErrNotFound
	}
	return t, err
}

func (s *Store) SetToolCallStatus(ctx context.Context, id int64, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE tool_calls SET status = $2 WHERE id = $1`, id, status)
	return err
}

func (s *Store) deleteOne(ctx context.Context, table string, id int64) error {
	// table is a constant from this file, never user input.
	tag, err := s.pool.Exec(ctx, `DELETE FROM `+table+` WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteCheckin removes a check-in row (undo of skip_checkin).
func (s *Store) DeleteCheckin(ctx context.Context, day time.Time, kind, characterID string) error {
	if err := requireOwner(characterID); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM checkins WHERE day = $1 AND kind = $2 AND character_id = $3`, Day(day), kind, characterID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
