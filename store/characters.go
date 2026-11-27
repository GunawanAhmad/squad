package store

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrCharacterExists is returned when a character id is already stored.
var ErrCharacterExists = errors.New("character id already exists")

// Characters returns the specs of characters made in the app, oldest first.
func (s *Store) Characters(ctx context.Context) ([]json.RawMessage, error) {
	rows, err := s.pool.Query(ctx, `SELECT spec FROM characters ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var specs []json.RawMessage
	for rows.Next() {
		var spec json.RawMessage
		if err := rows.Scan(&spec); err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, rows.Err()
}

// AddCharacter stores one character's spec under its id.
func (s *Store) AddCharacter(ctx context.Context, id string, spec json.RawMessage) error {
	tag, err := s.pool.Exec(ctx, `INSERT INTO characters (id, spec) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`, id, spec)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCharacterExists
	}
	return nil
}
