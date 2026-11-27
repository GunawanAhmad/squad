package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Room is a group conversation: at least two characters plus the user. It owns
// one conversation per member (and `me` for the transcript), so every
// per-character query keeps its isolation.
type Room struct {
	ID        string
	Name      string
	Members   []string
	Session   int // bumped by NewRoomSession
	CreatedAt time.Time
}

// ConversationID is the conversation a participant sees the room's current
// session through. Session 0 keeps the original id, so rooms made before
// sessions keep their history. The id stays within conversationIDRe.
func (r Room) ConversationID(participant string) string {
	if r.Session == 0 {
		return "room:" + r.ID + ":" + participant
	}
	return fmt.Sprintf("room:%s:%d:%s", r.ID, r.Session, participant)
}

// CreateRoom stores a room and one conversation per member plus the user's
// transcript, all in one transaction. The caller validates the members.
func (s *Store) CreateRoom(ctx context.Context, name string, members []string) (Room, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Room{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	r := Room{Name: name, Members: append([]string(nil), members...)}
	for i := 0; i < 8; i++ {
		id, err := newRoomID()
		if err != nil {
			return Room{}, err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO rooms (id, name, members) VALUES ($1, $2, $3)
			ON CONFLICT (id) DO NOTHING
			RETURNING id, created_at`, id, name, members).Scan(&r.ID, &r.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // id collision: draw again
		}
		if err != nil {
			return Room{}, err
		}
		break
	}
	if r.ID == "" {
		return Room{}, errors.New("room: could not allocate an id")
	}

	if err := createRoomConversations(ctx, tx, r); err != nil {
		return Room{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Room{}, err
	}
	return r, nil
}

// NewRoomSession starts the room over: the session number goes up and every
// participant gets an empty copy. Old sessions stay stored, so each character
// still remembers them the way it remembers an old 1:1 chat.
func (s *Store) NewRoomSession(ctx context.Context, id string) (Room, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Room{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	r, err := scanRoom(tx.QueryRow(ctx, `UPDATE rooms SET session = session + 1 WHERE id = $1 RETURNING `+roomCols, id))
	if err != nil {
		return Room{}, err
	}
	if err := createRoomConversations(ctx, tx, r); err != nil {
		return Room{}, err
	}
	return r, tx.Commit(ctx)
}

// createRoomConversations makes one conversation per member plus the user's
// transcript for the room's current session.
func createRoomConversations(ctx context.Context, tx pgx.Tx, r Room) error {
	for _, p := range append(append([]string(nil), r.Members...), UserSpeaker) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO conversations (id, character_id, room_id) VALUES ($1, $2, $3)`,
			r.ConversationID(p), p, r.ID); err != nil {
			return fmt.Errorf("room conversation for %s: %w", p, err)
		}
	}
	return nil
}

func newRoomID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

const roomCols = `id, name, members, session, created_at`

func scanRoom(row pgx.Row) (Room, error) {
	var r Room
	err := row.Scan(&r.ID, &r.Name, &r.Members, &r.Session, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Room{}, ErrNotFound
	}
	return r, err
}

// GetRoom returns one room, ErrNotFound when it does not exist.
func (s *Store) GetRoom(ctx context.Context, id string) (Room, error) {
	return scanRoom(s.pool.QueryRow(ctx, `SELECT `+roomCols+` FROM rooms WHERE id = $1`, id))
}

// ListRooms returns every room, newest first.
func (s *Store) ListRooms(ctx context.Context) ([]Room, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+roomCols+` FROM rooms ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Room
	for rows.Next() {
		r, err := scanRoom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
