package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"squad/assistant"
	"squad/llm"
	"squad/store"
	"squad/tools"
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
	// Context is this character's own memory stack: "memory" (default) means
	// profile.md plus its own core facts and pre-retrieval; "none" means none.
	// Memory is never shared between characters.
	Context string `json:"context"`
}

// The assistant is the one character with daily threads and domain tools.
const assistantCharacterID = "assistant"

// webTools are the only tools that read the outside world. Personas get these
// plus memoryTools (their own memory only); they never get the daily-domain
// tools. The assistant may use them too; every character's turn loop refuses
// memory writes after a web read (D3).
var webTools = []string{"web_search", "fetch_url"}

// memoryTools write and read the calling character's own facts. Personas get
// them scoped to themselves by the store, never the assistant's.
var memoryTools = []string{"remember", "forget", "recall"}

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
		chars[c.ID] = c
	}
	if err := validateCharacters(list, toolRegistry); err != nil {
		log.Fatal(err)
	}

	db, err := store.Open(context.Background(), dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Phase 2: the old memory/<char>.json notes become facts once. The profile
	// stays a file. Nothing from memory/ is ever printed.
	if n, err := importMemoryFacts(context.Background(), db, memDir); err != nil {
		log.Printf("memory: import skipped: %v", err)
	} else if n > 0 {
		log.Printf("memory: imported %d facts from %s", n, memDir)
	}

	// Semantic search (M-10): opt-in until the keyword-vs-hybrid comparison
	// is recorded. With EMBEDDINGS_URL unset, search is keyword-only and no
	// worker runs. If Ollama is down, embeddings wait and search still works.
	var embedder llm.Embedder
	if url := os.Getenv("EMBEDDINGS_URL"); url != "" {
		embedder = llm.NewOpenAIEmbeddings(url, "ollama", envOr("EMBEDDINGS_MODEL", "bge-m3"), 1024)
		go (&assistant.EmbedWorker{Store: db, Embedder: embedder}).Run(context.Background())
	}

	backup := newDailyBackup(backupDir)
	runner := assistant.New(assistant.Config{
		Store: db,
		Tools: toolRegistry,
		Models: map[string]llm.Model{
			string(llm.OpenCodeProvider): openCodeModel(),
			string(llm.ClaudeProvider):   claudeModel(),
		},
		DB:         db,
		Embedder:   embedder,
		Memory:     characterMemory(chars, db),
		Settings:   func(ctx context.Context) assistant.Settings { return assistant.LoadSettings(ctx, db) },
		Now:        assistant.Now,
		Location:   assistant.TimeZone(),
		BeforeTurn: func(ctx context.Context) { backup.Maybe(ctx, assistant.Now()) },
	})

	var engine *assistant.Engine
	if c, ok := chars[assistantCharacterID]; ok {
		provider, model, err := resolveModel(c, "")
		if err != nil {
			log.Fatal(err)
		}
		engine = &assistant.Engine{
			Store: db, Runner: runner, Now: assistant.Now, MemoryEnabled: memoryEnabled(chars),
			Char: assistant.Character{
				ID: c.ID, Name: c.Name, Provider: provider, Model: model,
				Persona: c.Persona, Tools: c.Tools, Context: c.Context, Daily: true,
			},
			Setting: func(ctx context.Context) assistant.Settings { return assistant.LoadSettings(ctx, db) },
			Before:  func(ctx context.Context, now time.Time) { backup.Maybe(ctx, now) },
		}
	}

	s := &server{chars: chars, list: list, db: db, records: db, runner: runner, engine: engine}
	mux := http.NewServeMux()
	s.routes(mux)

	if os.Getenv("DEV") != "" {
		// `make dev` proxies the UI to Vite so edits hot-reload at one URL;
		// the last `make web` build answers while Vite is restarting.
		target, err := url.Parse(envOr("VITE_URL", "http://127.0.0.1:5173"))
		if err != nil {
			log.Fatalf("VITE_URL: %v", err)
		}
		mux.Handle("GET /", devUI(target, http.FileServerFS(os.DirFS("web"))))
	} else {
		web, _ := fs.Sub(webFS, "web")
		mux.Handle("GET /", http.FileServerFS(web))
	}

	addr := envOr("ADDR", "127.0.0.1:8080") // localhost only: there is no auth, anyone who can reach it spends your quota
	log.Printf("listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// devUI hands the UI to the Vite dev server, which is what makes a saved .vue
// file show up without a rebuild, and falls back to the last built web/ while
// Vite is down or restarting.
func devUI(target *url.URL, fallback http.Handler) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("dev ui: vite at %s: %v (serving web/)", target, err)
		fallback.ServeHTTP(w, r)
	}
	return proxy
}

// memoryEnabled reports which characters have their memory stack on. A
// character set to context "none" is skipped by the day-close job too; an
// unknown id (removed from characters.json) is not given new facts.
func memoryEnabled(chars map[string]Character) func(string) bool {
	return func(charID string) bool {
		c, ok := chars[charID]
		return ok && c.Context != "none"
	}
}

// characterMemory is the always-in-prompt block for one agent: the shared
// profile.md the user writes, plus that agent's own core facts. Context "none"
// turns it off; another agent's facts are never part of it.
func characterMemory(chars map[string]Character, db *store.Store) func(string) string {
	return func(charID string) string {
		c, ok := chars[charID]
		if !ok || c.Context == "none" {
			return ""
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		facts, err := db.CoreFacts(ctx, charID, 100)
		if err != nil {
			log.Printf("memory: reading core facts for %s: %v", charID, err)
		}
		return memoryPrompt(readProfile(), facts)
	}
}

// validateCharacters keeps the config honest at startup: known tools, the
// personas that cannot write the user's day,
// a valid context setting, and a picker that includes the default model.
func validateCharacters(list []Character, reg *tools.Registry) error {
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
			if c.ID != assistantCharacterID && !slices.Contains(webTools, t) && !slices.Contains(memoryTools, t) {
				return fmt.Errorf("character %q: personas get no domain tool %q", c.ID, t)
			}
		}
		switch c.Context {
		case "", "memory", "none":
		default:
			return fmt.Errorf("character %q: context must be memory or none, not %q", c.ID, c.Context)
		}
		if len(c.Models) > 0 && !slices.Contains(c.Models, choice(c.Provider, c.Model)) {
			return fmt.Errorf("character %q: models must include %q", c.ID, choice(c.Provider, c.Model))
		}
	}
	return nil
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
