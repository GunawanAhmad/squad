package character

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sync"

	"squad/store"
	"squad/tools"
)

// Roster is the live character list. characters.json and the characters table
// fill it at startup; the app adds to it at runtime, so every read goes
// through the lock.
type Roster struct {
	mu   sync.RWMutex
	list []Character
	reg  *tools.Registry
	// Save persists a character before it joins the roster; nil keeps it in
	// memory only (startup loading and tests).
	Save func(context.Context, Character) error
}

// ErrInvalid marks an add refused by validation, not by storage.
var ErrInvalid = errors.New("invalid character")

func NewRoster(list []Character, reg *tools.Registry) *Roster {
	return &Roster{list: slices.Clone(list), reg: reg}
}

// Load merges the characters made in the app into the ones from
// characters.json. A stored character that no longer validates (say, its id
// is now taken in characters.json) is skipped with a log, not fatal.
func Load(ctx context.Context, list []Character, reg *tools.Registry, db *store.Store) (*Roster, error) {
	r := NewRoster(list, reg)
	specs, err := db.Characters(ctx)
	if err != nil {
		return nil, fmt.Errorf("characters table: %w", err)
	}
	for _, spec := range specs {
		var c Character
		if err := json.Unmarshal(spec, &c); err != nil {
			log.Printf("characters: skipping a stored character: %v", err)
			continue
		}
		if err := r.Add(ctx, c); err != nil {
			log.Printf("characters: skipping stored %q: %v", c.ID, err)
		}
	}
	r.Save = func(ctx context.Context, c Character) error {
		spec, err := json.Marshal(c)
		if err != nil {
			return err
		}
		return db.AddCharacter(ctx, c.ID, spec)
	}
	return r, nil
}

func (r *Roster) Get(id string) (Character, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, c := range r.list {
		if c.ID == id {
			return c, true
		}
	}
	return Character{}, false
}

func (r *Roster) All() []Character {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.list)
}

// Add validates c against everyone already here, saves it, then lists it.
func (r *Roster) Add(ctx context.Context, c Character) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := Validate(append(slices.Clone(r.list), c), r.reg); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if r.Save != nil {
		if err := r.Save(ctx, c); err != nil {
			return err
		}
	}
	r.list = append(r.list, c)
	return nil
}

// AssistantModel is the model id the room share gate runs with: the assistant's
// model from characters.json, unless settings.jobs_model overrides it.
func (r *Roster) AssistantModel() string {
	if c, ok := r.Get(AssistantID); ok {
		return c.Model
	}
	return ""
}

// MemoryEnabled reports whether a character has its memory stack on. A
// character set to context "none" is skipped by the day-close job too; an
// unknown id (removed from characters.json) is not given new facts.
func (r *Roster) MemoryEnabled(charID string) bool {
	c, ok := r.Get(charID)
	return ok && c.Context != "none"
}
