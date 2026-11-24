package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"

	"characters/assistant"
	"characters/llm"
	"characters/store"
	"characters/tools"
)

//go:embed web
var webFS embed.FS

type Character struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	UI       json.RawMessage `json:"ui"` // look, stats and voice; passed to the frontend as-is
	Persona  string          `json:"persona"`
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
	Models   []string        `json:"models"` // optional picker choices (see choice); must include the default
	Tools    []string        `json:"tools"`
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	CharacterID string    `json:"characterId"`
	SessionID   string    `json:"sessionId"`
	Messages    []chatMsg `json:"messages"`
	Model       string    `json:"model"`
}

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// Stateless: the tools themselves keep no state (store-backed tools arrive in L-2).
var toolRegistry = tools.New()

func main() {
	loadDotEnv(".env")
	if os.Getenv("OPENCODE_API_KEY") == "" {
		log.Fatal("OPENCODE_API_KEY is not set (see .env.example)")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is not set (see .env.example)")
	}

	raw, err := os.ReadFile("characters.json")
	if err != nil {
		log.Fatal(err)
	}
	var list []Character
	if err := json.Unmarshal(raw, &list); err != nil {
		log.Fatal("characters.json: ", err)
	}
	chars := map[string]Character{}
	for _, c := range list {
		for _, t := range c.Tools {
			if !toolRegistry.Has(t) {
				log.Fatalf("character %q: unknown tool %q", c.ID, t)
			}
		}
		if len(c.Models) > 0 && !slices.Contains(c.Models, choice(c.Provider, c.Model)) {
			log.Fatalf("character %q: models must include %q", c.ID, choice(c.Provider, c.Model))
		}
		chars[c.ID] = c
	}

	db, err := store.Open(context.Background(), dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	runner := assistant.New(assistant.Config{
		Store: db,
		Tools: toolRegistry,
		Models: map[string]llm.Model{
			string(llm.OpenCodeProvider): openCodeModel(),
			string(llm.ClaudeProvider):   claudeModel(),
		},
		Memory:   func(charID string) string { return memoryPrompt(Character{ID: charID}) },
		Now:      assistant.Now,
		Location: assistant.TimeZone(),
	})

	s := &server{chars: chars, list: list, db: db, runner: runner}
	mux := http.NewServeMux()
	s.routes(mux)

	web, _ := fs.Sub(webFS, "web")
	if os.Getenv("DEV") != "" {
		web = os.DirFS("web") // serve web/ from disk, so `make web` shows up without restarting
	}
	mux.Handle("GET /", http.FileServerFS(web))

	addr := envOr("ADDR", "127.0.0.1:8080") // localhost only: there is no auth, anyone who can reach it spends your quota
	log.Printf("listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func validate(req chatRequest) string {
	if !sessionIDRe.MatchString(req.SessionID) {
		return "bad sessionId"
	}
	n := len(req.Messages)
	if n == 0 || n > 200 {
		return "messages must have 1-200 items"
	}
	for _, m := range req.Messages {
		if (m.Role != "user" && m.Role != "assistant") || len(m.Content) > 50_000 {
			return "bad message"
		}
	}
	if req.Messages[n-1].Role != "user" {
		return "last message must be from user"
	}
	return ""
}

// choice is how a provider+model pair appears in the picker: "claude/opus", or a bare opencode model name.
func choice(provider, model string) string {
	if provider == "claude" {
		return "claude/" + model
	}
	return model
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// loadDotEnv sets KEY=VALUE lines from path; real env vars win.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}

	defer func() {
		err := f.Close()
		if err != nil {
			return
		}
	}()

	s := bufio.NewScanner(f)
	if s.Err() != nil {
		return
	}
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		k = strings.TrimSpace(k)
		if os.Getenv(k) == "" {
			err = os.Setenv(k, strings.Trim(strings.TrimSpace(v), `"'`))
			if err != nil {
				return
			}
		}
	}
}
