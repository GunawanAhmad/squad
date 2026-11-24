// Package tools holds the tools the model can call. Each tool parses and
// validates its own typed arguments; the registry only routes and enforces the
// character's allowlist.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"characters/llm"
	"characters/store"
)

// Env is what a tool may know about the turn it runs in.
type Env struct {
	CharacterID     string
	ConversationID  string
	Allowed         []string // the character's tool allowlist
	Now             time.Time
	DB              *store.Store   // domain tools only; nil in web-tool tests
	Day             time.Time      // logical day (04:00 boundary)
	Loc             *time.Location // settings.tz
	ToolCallID      int64          // this call's tool_calls row, stamped on records
	SourceMessageID *int64         // the user message that triggered the turn
}

// Result is what a tool returns: text for the model and, for write tools, the
// inverse operations the undo path applies.
type Result struct {
	Text string
	Undo []UndoOp
}

func text(s string) Result { return Result{Text: s} }

// Tool is one callable tool. Parameters is a JSON schema object.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Run         func(ctx context.Context, env Env, raw string) (Result, error)
}

// Registry is the set of all known tools.
type Registry struct {
	tools map[string]*Tool
}

func New() *Registry {
	r := &Registry{tools: map[string]*Tool{}}
	for _, t := range []*Tool{
		webSearchTool(), fetchURLTool(),
		planTasksTool(), updateTaskTool(), completeTaskTool(), listTasksTool(),
		logActivityTool(), queryActivitiesTool(),
		remindMeTool(), listRemindersTool(), cancelReminderTool(),
		undoTool(),
	} {
		r.tools[t.Name] = t
	}
	return r
}

// Add registers a tool (used by tests and later phases).
func (r *Registry) Add(t *Tool) {
	r.tools[t.Name] = t
}

func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// Specs returns the specs for the allowed tools, in allowlist order.
func (r *Registry) Specs(allowed []string) []llm.ToolSpec {
	out := make([]llm.ToolSpec, 0, len(allowed))
	for _, name := range allowed {
		t, ok := r.tools[name]
		if !ok {
			continue
		}
		params, err := json.Marshal(t.Parameters)
		if err != nil {
			continue
		}
		out = append(out, llm.ToolSpec{Name: t.Name, Description: t.Description, Parameters: params})
	}
	return out
}

// Run dispatches one call. It never panics on bad model output; the caller
// turns the error into text for the model so it can recover.
func (r *Registry) Run(ctx context.Context, env Env, name, raw string) (Result, error) {
	if !slices.Contains(env.Allowed, name) {
		return Result{}, fmt.Errorf("tool not available: %s", name)
	}
	t, ok := r.tools[name]
	if !ok {
		return Result{}, fmt.Errorf("unknown tool: %s", name)
	}
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	return t.Run(ctx, env, raw)
}

func webSearchTool() *Tool {
	return &Tool{
		Name:        "web_search",
		Description: "Search the web. Returns the top results with title, URL and a short snippet.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"query": map[string]any{"type": "string"}},
			"required":   []string{"query"},
		},
		Run: func(ctx context.Context, _ Env, raw string) (Result, error) {
			var args struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			if strings.TrimSpace(args.Query) == "" {
				return Result{}, errors.New("query is required")
			}
			out, err := webSearch(ctx, args.Query)
			return text(out), err
		},
	}
}

func fetchURLTool() *Tool {
	return &Tool{
		Name:        "fetch_url",
		Description: "Fetch a web page and return its readable text. Use it to read a result from web_search in full.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"url": map[string]any{"type": "string"}},
			"required":   []string{"url"},
		},
		Run: func(ctx context.Context, _ Env, raw string) (Result, error) {
			var args struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return Result{}, fmt.Errorf("bad arguments: %w", err)
			}
			if strings.TrimSpace(args.URL) == "" {
				return Result{}, errors.New("url is required")
			}
			out, err := fetchURL(ctx, args.URL)
			return text(out), err
		},
	}
}
