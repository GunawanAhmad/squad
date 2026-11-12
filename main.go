package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
)

//go:embed web
var webFS embed.FS

type Character struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Emoji   string   `json:"emoji"`
	Blurb   string   `json:"blurb"`
	Persona string   `json:"persona"`
	Model   string   `json:"model"`
	Tools   []string `json:"tools"`
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	CharacterID string    `json:"characterId"`
	SessionID   string    `json:"sessionId"`
	Messages    []chatMsg `json:"messages"`
}

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

func main() {
	loadDotEnv(".env")
	if os.Getenv("OPENCODE_API_KEY") == "" {
		log.Fatal("OPENCODE_API_KEY is not set (see .env.example)")
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
			if _, ok := toolSpecs[t]; !ok {
				log.Fatalf("character %q: unknown tool %q", c.ID, t)
			}
		}
		chars[c.ID] = c
	}

	web, _ := fs.Sub(webFS, "web")
	if os.Getenv("DEV") != "" {
		web = os.DirFS("web") // edit web/index.html and just refresh the browser
	}
	http.Handle("GET /", http.FileServerFS(web))

	http.HandleFunc("GET /api/characters", func(w http.ResponseWriter, r *http.Request) {
		public := make([]map[string]string, 0, len(list))
		for _, c := range list {
			public = append(public, map[string]string{"id": c.ID, "name": c.Name, "emoji": c.Emoji, "blurb": c.Blurb})
		}
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(public)
		if err != nil {
			return
		}
	})

	http.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		c, ok := chars[req.CharacterID]
		if !ok {
			http.Error(w, "unknown character", http.StatusBadRequest)
			return
		}
		if msg := validate(req); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		rc := http.NewResponseController(w)
		enc := json.NewEncoder(w)
		send := func(e event) {
			err := enc.Encode(e)
			if err != nil {
				return
			}

			err = rc.Flush()
			if err != nil {
				return
			}
		}
		if err := runChat(r.Context(), c, req.SessionID, req.Messages, send); err != nil && r.Context().Err() == nil {
			log.Printf("chat %s: %v", c.ID, err)
			send(event{Type: "error", Message: err.Error()})
		}
	})

	addr := envOr("ADDR", "127.0.0.1:8080") // localhost only: there is no auth, anyone who can reach it spends your quota
	log.Printf("listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
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
