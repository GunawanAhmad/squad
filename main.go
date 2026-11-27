package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"squad/assistant"
	"squad/backup"
	"squad/character"
	"squad/llm"
	"squad/server"
	"squad/store"
	"squad/tools"
)

//go:embed web
var webFS embed.FS

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
	var list []character.Character
	if err := json.Unmarshal(raw, &list); err != nil {
		log.Fatal("characters.json: ", err)
	}
	if err := character.Validate(list, toolRegistry); err != nil {
		log.Fatal(err)
	}

	db, err := store.Open(context.Background(), dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	chars, err := character.Load(context.Background(), list, toolRegistry, db)
	if err != nil {
		log.Fatal(err)
	}

	// Semantic search (M-10): opt-in until the keyword-vs-hybrid comparison
	// is recorded. With EMBEDDINGS_URL unset, search is keyword-only and no
	// worker runs. If Ollama is down, embeddings wait and search still works.
	var embedder llm.Embedder
	if url := os.Getenv("EMBEDDINGS_URL"); url != "" {
		embedder = llm.NewOpenAIEmbeddings(url, "ollama", envOr("EMBEDDINGS_MODEL", "bge-m3"), 1024)
		go (&assistant.EmbedWorker{Store: db, Embedder: embedder}).Run(context.Background())
	}

	backup := backup.New(backup.Dir)
	runner := assistant.New(assistant.Config{
		Store: db,
		Tools: toolRegistry,
		Models: map[string]llm.Model{
			string(llm.OpenCodeProvider): openCodeModel(),
			string(llm.ClaudeProvider):   claudeModel(),
		},
		DB:         db,
		Embedder:   embedder,
		Memory:     character.Memory(chars, db),
		Profile:    character.ProfilePrompt,
		GateModel:  chars.AssistantModel(),
		Settings:   func(ctx context.Context) assistant.Settings { return assistant.LoadSettings(ctx, db) },
		Now:        assistant.Now,
		Location:   assistant.TimeZone(),
		BeforeTurn: func(ctx context.Context) { backup.Maybe(ctx, assistant.Now()) },
	})

	var engine *assistant.Engine
	if c, ok := chars.Get(character.AssistantID); ok {
		provider, model, err := character.ResolveModel(c, "")
		if err != nil {
			log.Fatal(err)
		}
		engine = &assistant.Engine{
			Store: db, Runner: runner, Now: assistant.Now, MemoryEnabled: chars.MemoryEnabled,
			Char:    character.ToAssistant(c, provider, model),
			Setting: func(ctx context.Context) assistant.Settings { return assistant.LoadSettings(ctx, db) },
			Before:  func(ctx context.Context, now time.Time) { backup.Maybe(ctx, now) },
		}
	}

	mux := http.NewServeMux()
	server.New(chars, db, runner, engine).Routes(mux)

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
