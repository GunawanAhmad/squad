package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"squad/assistant"
	"squad/llm"
)

// characterServer has an assistant (whose model drafts) and a scripted model.
func characterServer(t *testing.T, steps ...llm.FakeStep) (*server, *[]Character) {
	t.Helper()
	fake := llm.NewFake(steps...)
	runner := assistant.New(assistant.Config{
		Store: newMemStore(), Tools: toolRegistry,
		Models: map[string]llm.Model{string(llm.OpenCodeProvider): fake},
	})
	r := newRoster([]Character{
		{ID: "assistant", Name: "Assistant", Model: "fast", Models: []string{"fast", "claude/opus"}, Tools: []string{"plan_tasks"}},
		{ID: "pip", Name: "Pip", Model: "fast"},
	})
	var saved []Character
	r.save = func(_ context.Context, c Character) error { saved = append(saved, c); return nil }
	return &server{roster: r, runner: runner}, &saved
}

func TestDraftCharacterClampsModelOutput(t *testing.T) {
	s, _ := characterServer(t, llm.FakeStep{Text: "```json\n" + `{"name":" Mara ","tagline":"Sails.","persona":"You are Mara.",
		"accentColor":"red","stats":[{"label":"Grit","value":14},{"label":"Discretion","value":2}],
		"voice":{"pitch":9},"discretion":12}` + "\n```"})
	w := doJSON(t, s.handler(), "POST", "/api/characters/draft", `{"description":"an old sea captain","tools":["web_search","recall"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("draft: %d %s", w.Code, w.Body)
	}
	var d charDraft
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Name != "Mara" || d.AccentColor != defaultAccent || d.Discretion != 10 || d.Voice.Pitch != 2 || d.Voice.Rate != 1 {
		t.Fatalf("draft not normalized: %+v", d)
	}
	if len(d.Stats) != 1 || d.Stats[0] != (charStat{"Grit", 10}) {
		t.Fatalf("stats: %+v (Discretion must come from the field, values 0-10)", d.Stats)
	}
	if strings.Join(d.Tools, ",") != "web_search,recall" || d.Model != "fast" {
		t.Fatalf("tools/model come from the request and the assistant: %v %q", d.Tools, d.Model)
	}
}

func TestDraftCharacterRefusesDailyTools(t *testing.T) {
	s, _ := characterServer(t)
	w := doJSON(t, s.handler(), "POST", "/api/characters/draft", `{"description":"x","tools":["plan_tasks"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
}

func TestCreateCharacterFillsServerFields(t *testing.T) {
	s, saved := characterServer(t)
	h := s.handler()
	body := `{"name":"Pip","persona":"You are another Pip.","discretion":3,"tools":["web_search","remember","recall"],"model":"claude/opus","stats":[{"label":"Wit","value":7}]}`
	w := doJSON(t, h, "POST", "/api/characters", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if len(*saved) != 1 {
		t.Fatalf("saved %d characters", len(*saved))
	}
	c := (*saved)[0]
	if c.ID != "pip-2" || c.Provider != "claude" || c.Model != "opus" || c.Context != "memory" || discretionOf(c) != 3 {
		t.Fatalf("server fields: %+v", c)
	}
	if !strings.HasPrefix(c.Persona, "You are another Pip.") || !strings.Contains(c.Persona, "use remember") || strings.Contains(c.Persona, "use forget") {
		t.Fatalf("persona memory paragraph follows the chosen tools: %q", c.Persona)
	}
	if !strings.Contains(string(c.UI), `{"label":"Discretion","value":3}`) {
		t.Fatalf("ui stats: %s", c.UI)
	}
	if _, ok := s.roster.get("pip-2"); !ok {
		t.Fatal("new character is not in the roster")
	}
	if w := doJSON(t, h, "GET", "/api/characters", ""); !strings.Contains(w.Body.String(), `"id":"pip-2"`) {
		t.Fatalf("list: %s", w.Body)
	}
}

func TestCreateCharacterRejects(t *testing.T) {
	cases := map[string]string{
		"daily tool":     `{"name":"X","persona":"p","tools":["plan_tasks"]}`,
		"unknown model":  `{"name":"X","persona":"p","model":"gpt-9"}`,
		"no persona":     `{"name":"X","persona":"  "}`,
		"no name":        `{"name":"","persona":"p"}`,
		"bad discretion": `{"name":"X","persona":"p","discretion":11}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, saved := characterServer(t)
			if w := doJSON(t, s.handler(), "POST", "/api/characters", body); w.Code != http.StatusBadRequest {
				t.Fatalf("status %d %s", w.Code, w.Body)
			}
			if len(*saved) != 0 {
				t.Fatal("a rejected character was saved")
			}
		})
	}
}

func TestCreateCharacterNeverTakesReservedIDs(t *testing.T) {
	s, _ := characterServer(t)
	for name, want := range map[string]string{"Me": "me-2", "Assistant": "assistant-2", "!!!": "character"} {
		if got := s.freeID(name); got != want {
			t.Fatalf("freeID(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestCreateCharacterSaveFailureKeepsRosterUnchanged(t *testing.T) {
	s, _ := characterServer(t)
	s.roster.save = func(context.Context, Character) error { return errors.New("db down") }
	w := doJSON(t, s.handler(), "POST", "/api/characters", `{"name":"Zed","persona":"p"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if _, ok := s.roster.get("zed"); ok {
		t.Fatal("unsaved character joined the roster")
	}
}
