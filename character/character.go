// Package character is the cast: the Character config, the rules every
// character must pass, and the live roster the app reads and adds to.
package character

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"squad/assistant"
	"squad/tools"
)

type Character struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	UI       json.RawMessage `json:"ui"` // look, stats and voice; passed to the frontend as-is
	Persona  string          `json:"persona"`
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
	Models   []string        `json:"models"` // optional picker choices (see Choice); must include the default
	Tools    []string        `json:"tools"`
	// Context is this character's own memory stack: "memory" (default) means
	// profile.md plus its own core facts and pre-retrieval; "none" means none.
	// Memory is never shared between characters.
	Context string `json:"context"`
	// Discretion is how much the character shares about the user in a room,
	// 0-10. Nil means the default (5).
	Discretion *int `json:"discretion"`
}

// DefaultDiscretion applies when characters.json does not set one.
const DefaultDiscretion = 5

// DiscretionOf returns the character's discretion, or the default.
func DiscretionOf(c Character) int {
	if c.Discretion == nil {
		return DefaultDiscretion
	}
	return *c.Discretion
}

// AssistantID is the one character with daily threads and domain tools.
const AssistantID = "assistant"

// WebTools are the only tools that read the outside world. Personas get these
// plus MemoryTools (their own memory only); they never get the daily-domain
// tools. The assistant may use them too; every character's turn loop refuses
// memory writes after a web read (D3).
var WebTools = []string{"web_search", "fetch_url"}

// MemoryTools write and read the calling character's own facts. Personas get
// them scoped to themselves by the store, never the assistant's.
var MemoryTools = []string{"remember", "forget", "recall"}

// ToAssistant maps a configured character to the turn runner's view.
// Every turn request (1:1, daily, room) goes through here so a new field is
// wired in one place.
func ToAssistant(c Character, provider, model string) assistant.Character {
	return assistant.Character{
		ID: c.ID, Name: c.Name, Provider: provider, Model: model, Persona: c.Persona,
		Tools: c.Tools, Context: c.Context, Daily: c.ID == AssistantID,
		Discretion: DiscretionOf(c),
	}
}

// Validate keeps the config honest at startup: known tools, the
// personas that cannot write the user's day,
// a valid context setting, and a picker that includes the default model.
func Validate(list []Character, reg *tools.Registry) error {
	seen := map[string]bool{}
	for _, c := range list {
		if c.ID == "" || c.Name == "" {
			return fmt.Errorf("character %q: id and name are required", c.ID)
		}
		if seen[c.ID] {
			return fmt.Errorf("character %q: duplicate id", c.ID)
		}
		seen[c.ID] = true
		for _, t := range c.Tools {
			if !reg.Has(t) {
				return fmt.Errorf("character %q: unknown tool %q", c.ID, t)
			}
			// Personas may read the web and keep their own memory; they never
			// get the daily-domain tools (that is the assistant's day).
			if c.ID != AssistantID && !slices.Contains(WebTools, t) && !slices.Contains(MemoryTools, t) {
				return fmt.Errorf("character %q: personas get no domain tool %q", c.ID, t)
			}
		}
		switch c.Context {
		case "", "memory", "none":
		default:
			return fmt.Errorf("character %q: context must be memory or none, not %q", c.ID, c.Context)
		}
		if c.Discretion != nil && (*c.Discretion < 0 || *c.Discretion > 10) {
			return fmt.Errorf("character %q: discretion must be 0-10, not %d", c.ID, *c.Discretion)
		}
		if len(c.Models) > 0 && !slices.Contains(c.Models, Choice(c.Provider, c.Model)) {
			return fmt.Errorf("character %q: models must include %q", c.ID, Choice(c.Provider, c.Model))
		}
	}
	return nil
}

// Choice is how a provider+model pair appears in the picker: "claude/opus", or a bare opencode model name.
func Choice(provider, model string) string {
	if provider == "claude" {
		return "claude/" + model
	}
	return model
}

// ResolveModel validates a picker choice against the character's allowlist and
// returns the provider/model to use.
func ResolveModel(c Character, requested string) (provider, model string, err error) {
	provider, model = c.Provider, c.Model
	if requested == "" || requested == Choice(c.Provider, c.Model) {
		return provider, model, nil
	}
	if !slices.Contains(c.Models, requested) {
		return "", "", errors.New("unknown model")
	}
	provider, model = "", requested
	if m, ok := strings.CutPrefix(requested, "claude/"); ok {
		provider, model = "claude", m
	}
	return provider, model, nil
}
