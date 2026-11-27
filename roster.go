package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
)

// roster is the live character list. characters.json and the characters table
// fill it at startup; the app adds to it at runtime, so every read goes
// through the lock.
type roster struct {
	mu   sync.RWMutex
	list []Character
	// save persists a character before it joins the roster; nil keeps it in
	// memory only (startup loading and tests).
	save func(context.Context, Character) error
}

// errInvalidCharacter marks an add refused by validation, not by storage.
var errInvalidCharacter = errors.New("invalid character")

func newRoster(list []Character) *roster {
	return &roster{list: slices.Clone(list)}
}

func (r *roster) get(id string) (Character, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, c := range r.list {
		if c.ID == id {
			return c, true
		}
	}
	return Character{}, false
}

func (r *roster) all() []Character {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.list)
}

// add validates c against everyone already here, saves it, then lists it.
func (r *roster) add(ctx context.Context, c Character) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateCharacters(append(slices.Clone(r.list), c), toolRegistry); err != nil {
		return fmt.Errorf("%w: %v", errInvalidCharacter, err)
	}
	if r.save != nil {
		if err := r.save(ctx, c); err != nil {
			return err
		}
	}
	r.list = append(r.list, c)
	return nil
}
