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
	CreatedAt time.Time
}

// RoomConversationID is the conversation a participant sees the room through.
// The id stays within conversationIDRe.
func RoomConversationID(roomID, participant string) string {
	return "room:" + roomID + ":" + participant
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

	participants := append(append([]string(nil), members...), UserSpeaker)
	for _, p := range participants {
		if _, err := tx.Exec(ctx, `
			INSERT INTO conversations (id, character_id, room_id) VALUES ($1, $2, $3)`,
			RoomConversationID(r.ID, p), p, r.ID); err != nil {
			return Room{}, fmt.Errorf("room conversation for %s: %w", p, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Room{}, err
	}
	return r, nil
}

func newRoomID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// GetRoom returns one room, ErrNotFound when it does not exist.
func (s *Store) GetRoom(ctx context.Context, id string) (Room, error) {
	var r Room
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, members, created_at FROM rooms WHERE id = $1`, id).
		Scan(&r.ID, &r.Name, &r.Members, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Room{}, ErrNotFound
	}
	return r, err
}

// ListRooms returns every room, newest first.
func (s *Store) ListRooms(ctx context.Context) ([]Room, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, members, created_at FROM rooms ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Room
	for rows.Next() {
		var r Room
		if err := rows.Scan(&r.ID, &r.Name, &r.Members, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
