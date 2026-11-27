// Package assistant runs one turn of a conversation: load context, call the
// model, execute tool calls, persist every message and a trace row. It knows
// nothing about HTTP; the handler supplies the stream sink.
package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"squad/llm"
	"squad/store"
	"squad/tools"
)

const (
	StatusRunning   = "running"
	StatusOK        = "ok"
	StatusError     = "error"
	StatusCancelled = "cancelled"
	StatusLimited   = "limited"
)

const stopNote = "\n\n(stopped: too many tool steps)"

// Store is the slice of the store the turn runner uses. *store.Store satisfies
// it; tests use an in-memory implementation so loop tests need no Postgres.
type Store interface {
	CreateConversation(ctx context.Context, c store.Conversation) (store.Conversation, error)
	AddMessage(ctx context.Context, m store.Message) (store.Message, error)
	RecentMessages(ctx context.Context, conversationID string, n int) ([]store.Message, error)
	LastSpeaker(ctx context.Context, conversationID string) (string, error)
	RecentMessagesAfter(ctx context.Context, conversationID string, after int64, n int) ([]store.Message, error)
	StartRun(ctx context.Context, r store.Run) (int64, error)
	FinishRun(ctx context.Context, id int64, status, errMsg string, inputTokens, outputTokens *int) error
	StartToolCall(ctx context.Context, tc store.ToolCall) (store.ToolCall, error)
	FinishToolCall(ctx context.Context, id int64, result string, undo []byte, status string) error
}

type Config struct {
	Store  Store
	Tools  *tools.Registry
	Models map[string]llm.Model
	// Memory is the always-in-prompt block for one character: profile.md plus
	// its own core facts. Nil means no memory block.
	Memory func(characterID string) string
	// Profile is the room-mode memory: profile.md only, nothing private. It
	// is not gated (the user wrote it for everyone).
	Profile func() string
	// GateModel is the model id the room share gate runs with; empty means the
	// character's own model. settings.jobs_model overrides it.
	GateModel string
	Now       func() time.Time
	Location  *time.Location
	Window    int // last N messages, default DefaultWindow
	MaxSteps  int // model calls per turn, default 10
	// BeforeTurn runs before the turn proceeds (e.g. the lazy daily backup).
	// It must not fail the turn; errors are the hook's own business.
	BeforeTurn func(ctx context.Context)
	// DB is handed to domain tools; nil means only web tools work (unit tests).
	DB *store.Store
	// Settings reads settings.tz and the day boundary; nil means defaults.
	Settings func(ctx context.Context) Settings
	// Embedder enables hybrid retrieval (M-10); nil means keyword-only.
	Embedder llm.Embedder
}

type Runner struct {
	store     Store
	tools     *tools.Registry
	models    map[string]llm.Model
	memory    func(string) string
	profile   func() string
	gateModel string
	now       func() time.Time
	loc       *time.Location
	window    int
	maxSteps  int

	beforeTurn func(context.Context)
	db         *store.Store
	settings   func(context.Context) Settings
	embedder   llm.Embedder

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func New(cfg Config) *Runner {
	r := &Runner{
		store:      cfg.Store,
		tools:      cfg.Tools,
		models:     cfg.Models,
		memory:     cfg.Memory,
		profile:    cfg.Profile,
		gateModel:  cfg.GateModel,
		now:        cfg.Now,
		loc:        cfg.Location,
		window:     cfg.Window,
		maxSteps:   cfg.MaxSteps,
		beforeTurn: cfg.BeforeTurn,
		db:         cfg.DB,
		settings:   cfg.Settings,
		embedder:   cfg.Embedder,
		locks:      map[string]*sync.Mutex{},
	}
	if r.tools == nil {
		r.tools = tools.New()
	}
	if r.now == nil {
		r.now = Now
	}
	if r.loc == nil {
		r.loc = TimeZone()
	}
	if r.window <= 0 {
		r.window = DefaultWindow
	}
	if r.maxSteps <= 0 {
		r.maxSteps = 10
	}
	return r
}

type TurnRequest struct {
	ConversationID string
	Character      Character
	Input          string
	Model          string // resolved picker model; empty means Character.Model
	Kind           string // runs.kind; empty means "turn"
	Role           string // stored role of Input: user (default) or trigger (check-ins)
	// Speaker is who said Input in a room (a character id, or "me"). Stored
	// raw on the message; the prompt labels it from Names.
	Speaker string
	// Names maps speaker ids to display names for room history.
	Names map[string]string
	// Listeners are the display names of the other characters present. A
	// non-empty list makes this a room turn: the profile replaces private
	// memory, memory candidates go through the share gate, and recall is off.
	Listeners []string
	// Participants are the character ids present (including the speaker), so a
	// remembered fact can be about any of them. Empty in 1:1 chats.
	Participants []string
}

// Event mirrors the NDJSON the frontend already knows.
type Event struct {
	Type    string `json:"type"` // text | tool | error
	Delta   string `json:"delta,omitempty"`
	Name    string `json:"name,omitempty"`
	Args    string `json:"args,omitempty"`
	Message string `json:"message,omitempty"`
	ID      int64  `json:"id,omitempty"` // tool_calls row, for the Undo button
}

type Result struct {
	ConversationID     string
	RunID              int64
	UserMessageID      int64
	AssistantMessageID int64
	Status             string
	Usage              llm.Usage
	// Text is the final assistant text of the turn (the last non-empty reply).
	Text string
}

// Turn runs one turn. Only one turn per conversation runs at a time; a second
// caller waits instead of interleaving (a check-in waits its turn).
func (r *Runner) Turn(ctx context.Context, req TurnRequest, emit func(Event)) (Result, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	if req.ConversationID == "" || req.Character.ID == "" {
		return Result{}, errors.New("turn: conversation id and character id are required")
	}
	if strings.TrimSpace(req.Input) == "" {
		return Result{}, errors.New("turn: empty input")
	}

	lock := r.lockFor(req.ConversationID)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if r.beforeTurn != nil {
		r.beforeTurn(ctx)
	}

	conv, err := r.store.CreateConversation(ctx, store.Conversation{
		ID: req.ConversationID, CharacterID: req.Character.ID,
	})
	if err != nil {
		return Result{}, err
	}
	if conv.CharacterID != req.Character.ID {
		return Result{}, fmt.Errorf("conversation %s belongs to character %s", conv.ID, conv.CharacterID)
	}

	model, err := r.modelFor(req.Character.Provider)
	if err != nil {
		return Result{}, err
	}
	modelName := req.Model
	if modelName == "" {
		modelName = req.Character.Model
	}

	var history []store.Message
	if conv.SummaryUpto != nil && *conv.SummaryUpto > 0 {
		history, err = r.store.RecentMessagesAfter(ctx, conv.ID, *conv.SummaryUpto, r.window)
	} else {
		history, err = r.store.RecentMessages(ctx, conv.ID, r.window)
	}
	if err != nil {
		return Result{}, err
	}
	room := len(req.Listeners) > 0
	var memory string
	if room {
		if r.profile != nil {
			memory = r.profile()
		}
	} else if r.memory != nil {
		memory = r.memory(req.Character.ID)
	}
	// In a room the character loses the tools that would bypass the gate or
	// expose the assistant's private day.
	toolNames := req.Character.Tools
	if room {
		toolNames = slices.DeleteFunc(slices.Clone(req.Character.Tools), func(n string) bool { return roomHiddenTools[n] })
	}
	now := r.now()
	set := r.settingsFor(ctx)
	day := LogicalDay(now, set.Loc, set.DayStartsAt)
	var records *Records
	// The day's records are private too: no plans, activities or reminders in
	// front of other characters.
	if rs, ok := r.store.(RecordsStore); ok && req.Character.Daily && !room {
		loaded := LoadRecords(ctx, rs, req.Character.ID, day, set)
		records = &loaded
	}
	// Every character retrieves from its own messages and facts; the memory
	// stack is not tied to the assistant's daily thread any more. Check-ins are
	// triggered instructions, not the user asking, so they retrieve nothing.
	var retrieved []Retrieved
	if searcher, ok := r.store.(Searcher); ok && req.Character.hasMemory() && req.Role != "trigger" {
		tz := set.TZ
		if tz == "" && set.Loc != nil {
			tz = set.Loc.String()
		}
		hits, err := Retrieve(ctx, searcher, req.Character.ID, req.Input, set.Loc, tz, cctxIDs(history), r.embedder)
		if err != nil {
			log.Printf("assistant: pre-retrieval: %v", err)
		} else {
			retrieved = hits
		}
	}

	var gate *gateTrace
	var gateUsage llm.Usage
	if room {
		retrieved, gate, gateUsage = r.roomGate(ctx, req.Character, req.Listeners, req.Input, modelName, set, retrieved)
	}
	cctx := BuildContext(ContextInput{
		Character: req.Character, Memory: memory, History: history, Summary: conv.Summary,
		Window: r.window, Now: now, Settings: set, Records: records, Retrieved: retrieved,
		Names: req.Names, Listeners: req.Listeners,
	})

	retrievedMessages, retrievedFacts := Retrieved{}.splitIDs(retrieved)
	traceDoc := contextTrace{
		Sections:            cctx.Sections,
		MessageIDs:          cctx.MessageIDs,
		Window:              cctx.Window,
		SystemChars:         len(cctx.System),
		RetrievedMessageIDs: retrievedMessages,
		RetrievedFactIDs:    retrievedFacts,
		SummaryUpto:         summaryUpto(conv),
		Truncated:           cctx.Truncated,
	}
	if gate != nil {
		traceDoc.GateSharedIDs = gate.SharedIDs
		traceDoc.GateKeptIDs = gate.KeptIDs
		traceDoc.GateError = gate.Err
	}
	trace, _ := json.Marshal(traceDoc)
	kind := req.Kind
	if kind == "" {
		kind = "turn"
	}
	runID, err := r.store.StartRun(ctx, store.Run{
		Kind: kind, ConversationID: &conv.ID, Model: modelName, Context: trace, Status: StatusRunning,
	})
	if err != nil {
		return Result{}, err
	}

	result := Result{ConversationID: conv.ID, RunID: runID, Status: StatusRunning}
	role := req.Role
	if role == "" {
		role = "user"
	}
	userMsg, err := r.store.AddMessage(ctx, store.Message{
		ConversationID: conv.ID, Role: role, Content: req.Input, Speaker: req.Speaker, RunID: &runID,
	})
	if err != nil {
		r.finish(ctx, runID, StatusError, err.Error(), llm.Usage{})
		return result, err
	}
	result.UserMessageID = userMsg.ID

	// The stored content is raw; the live copy gets the same speaker label the
	// next turn will read from history.
	msgs := append(cctx.Messages, llm.Message{Role: role, Content: speakerLine(role, req.Speaker, req.Input, req.Names)})
	env := tools.Env{
		CharacterID: req.Character.ID, ConversationID: conv.ID,
		Allowed: toolNames, Now: now, Speaker: req.Speaker,
		FactSubjects: factSubjects(req.Character.ID, req.Participants),
		DB:           r.db, Loc: set.Loc, Day: day, DayStartsAt: set.DayStartsAt,
		SourceMessageID: &userMsg.ID, Embedder: r.embedder,
	}

	var usage llm.Usage
	var lastText string // the last words actually spoken (for errors and the cap)
	usage.InputTokens += gateUsage.InputTokens
	usage.OutputTokens += gateUsage.OutputTokens
	webRead := false // a page or search result may carry planted instructions
	for step := 0; step < r.maxSteps; step++ {
		var partial strings.Builder
		resp, err := model.Stream(ctx, llm.Request{
			Model: modelName, System: cctx.System, Messages: msgs,
			Tools: r.tools.Specs(toolNames), Session: conv.ID,
		}, func(text string) {
			partial.WriteString(text)
			emit(Event{Type: "text", Delta: text})
		})
		if err != nil {
			if partial.Len() > 0 {
				if m, saveErr := r.addAssistant(ctx, conv.ID, runID, partial.String(), nil); saveErr == nil {
					result.AssistantMessageID = m.ID
				}
			}
			status := StatusError
			if ctx.Err() != nil {
				status = StatusCancelled
			}
			r.finish(ctx, runID, status, err.Error(), usage)
			result.Status = status
			result.Usage = usage
			result.Text = lastText
			return result, err
		}

		usage.InputTokens += resp.Usage.InputTokens
		usage.OutputTokens += resp.Usage.OutputTokens
		if resp.Text != "" {
			lastText = resp.Text
		}
		// Fill missing ids before persisting, so the stored assistant call and
		// the stored tool result carry the same id.
		for i := range resp.ToolCalls {
			if resp.ToolCalls[i].ID == "" {
				resp.ToolCalls[i].ID = fmt.Sprintf("call_%d_%d", runID, i)
			}
		}

		if resp.Text != "" || len(resp.ToolCalls) > 0 {
			m, err := r.addAssistant(ctx, conv.ID, runID, resp.Text, resp.ToolCalls)
			if err != nil {
				r.finish(ctx, runID, StatusError, err.Error(), usage)
				result.Status = StatusError
				return result, err
			}
			result.AssistantMessageID = m.ID
		}

		if len(resp.ToolCalls) == 0 {
			// The terminal answer is the final reply; a preamble before a tool
			// call is not.
			result.Text = resp.Text
			r.finish(ctx, runID, StatusOK, "", usage)
			result.Status = StatusOK
			result.Usage = usage
			return result, nil
		}

		msgs = append(msgs, llm.Message{
			Role: "assistant", Content: resp.Text, ToolCalls: resp.ToolCalls, Reasoning: resp.Reasoning,
		})
		for _, tc := range resp.ToolCalls {
			// The row exists before the tool runs, so a write tool can stamp the
			// records it creates with this id (the Today panel's undo key).
			call, err := r.store.StartToolCall(ctx, store.ToolCall{
				RunID: runID, Name: tc.Name, Args: traceArgs(tc.Arguments),
			})
			if err != nil {
				r.finish(ctx, runID, StatusError, err.Error(), usage)
				result.Status = StatusError
				return result, err
			}
			emit(Event{Type: "tool", Name: tc.Name, Args: tc.Arguments, ID: call.ID})

			env.ToolCallID = call.ID
			var res tools.Result
			var toolErr error
			if webRead && (tc.Name == "remember" || tc.Name == "forget" || tc.Name == "undo") {
				// D3: web content must never reach memory directly. The user can say it in their next message.
				toolErr = errors.New("refused: memory can't change in the same turn as reading the web; ask the user to confirm it in their own message")
			} else {
				res, toolErr = r.tools.Run(ctx, env, tc.Name, tc.Arguments)
			}
			if tc.Name == "web_search" || tc.Name == "fetch_url" {
				webRead = true
			}
			status, out := "ok", res.Text
			var undo []byte
			if toolErr != nil {
				status = "error"
				out = "error: " + toolErr.Error()
			} else if len(res.Undo) > 0 {
				undo, _ = json.Marshal(res.Undo)
			}
			if err := r.store.FinishToolCall(ctx, call.ID, out, undo, status); err != nil {
				r.finish(ctx, runID, StatusError, err.Error(), usage)
				result.Status = StatusError
				return result, err
			}
			content := out
			if undo != nil {
				content += fmt.Sprintf("\n\n(undo: tool_call_id=%d)", call.ID)
			}
			if _, err := r.store.AddMessage(ctx, store.Message{
				ConversationID: conv.ID, Role: "tool", Content: content, ToolCallID: tc.ID, RunID: &runID,
			}); err != nil {
				r.finish(ctx, runID, StatusError, err.Error(), usage)
				result.Status = StatusError
				return result, err
			}
			msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: content})
		}
	}

	emit(Event{Type: "text", Delta: stopNote})
	m, err := r.addAssistant(ctx, conv.ID, runID, stopNote, nil)
	if err != nil {
		r.finish(ctx, runID, StatusError, err.Error(), usage)
		result.Status = StatusError
		return result, err
	}
	result.AssistantMessageID = m.ID
	r.finish(ctx, runID, StatusLimited, "", usage)
	result.Status = StatusLimited
	result.Usage = usage
	result.Text = lastText
	return result, nil
}

// Model returns the adapter for a provider (the background jobs run on the
// OpenCode route, where app tools work).
func (r *Runner) Model(provider llm.Provider) (llm.Model, bool) {
	m, ok := r.models[string(provider)]
	return m, ok
}

func (r *Runner) modelFor(provider string) (llm.Model, error) {
	if provider == "" {
		provider = string(llm.OpenCodeProvider)
	}
	m, ok := r.models[provider]
	if !ok {
		return nil, fmt.Errorf("no model adapter for provider %q", provider)
	}
	return m, nil
}

// settingsFor is the runner's view of settings.tz and the day boundary.
func (r *Runner) settingsFor(ctx context.Context) Settings {
	if r.settings != nil {
		return r.settings(ctx)
	}
	set := DefaultSettings()
	if r.loc != nil {
		set.Loc, set.TZ = r.loc, r.loc.String()
	}
	return set
}

// Day is the logical day of now (used by tools and the context builder).
func (r *Runner) Day(ctx context.Context) time.Time {
	set := r.settingsFor(ctx)
	return LogicalDay(r.now(), set.Loc, set.DayStartsAt)
}

func (r *Runner) lockFor(conversationID string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.locks[conversationID]
	if m == nil {
		m = &sync.Mutex{}
		r.locks[conversationID] = m
	}
	return m
}

func (r *Runner) addAssistant(ctx context.Context, convID string, runID int64, text string, calls []llm.ToolCall) (store.Message, error) {
	var raw []byte
	if len(calls) > 0 {
		raw, _ = json.Marshal(calls)
	}
	return r.store.AddMessage(ctx, store.Message{
		ConversationID: convID, Role: "assistant", Content: text, ToolCalls: raw, RunID: &runID,
	})
}

// finish records the run even when the request context is already canceled:
// a cancelled turn must not stay "running" forever.
func (r *Runner) finish(ctx context.Context, runID int64, status, errMsg string, usage llm.Usage) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	var in, out *int
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		i, o := usage.InputTokens, usage.OutputTokens
		in, out = &i, &o
	}
	if err := r.store.FinishRun(fctx, runID, status, errMsg, in, out); err != nil {
		// Nothing else to do: the run row stays running, which is visible.
		log.Printf("assistant: finishing run %d: %v", runID, err)
	}
}

type contextTrace struct {
	Sections            []string `json:"sections"`
	MessageIDs          []int64  `json:"message_ids"`
	RetrievedMessageIDs []int64  `json:"retrieved_message_ids,omitempty"`
	RetrievedFactIDs    []int64  `json:"retrieved_fact_ids,omitempty"`
	Window              int      `json:"window"`
	SummaryUpto         int64    `json:"summary_upto,omitempty"`
	Truncated           []string `json:"truncated,omitempty"`
	SystemChars         int      `json:"system_chars"`
	GateSharedIDs       []string `json:"gate_shared_ids,omitempty"`
	GateKeptIDs         []string `json:"gate_kept_ids,omitempty"`
	GateError           string   `json:"gate_error,omitempty"`
}

func summaryUpto(c store.Conversation) int64 {
	if c.SummaryUpto == nil {
		return 0
	}
	return *c.SummaryUpto
}

// cctxIDs lists the ids already in the window so retrieval can skip them.
func cctxIDs(history []store.Message) []int64 {
	ids := make([]int64, 0, len(history))
	for _, m := range history {
		ids = append(ids, m.ID)
	}
	return ids
}

// traceArgs keeps a model's malformed arguments out of the jsonb column.
func traceArgs(raw string) []byte {
	if json.Valid([]byte(raw)) {
		return []byte(raw)
	}
	b, _ := json.Marshal(map[string]string{"invalid_arguments": raw})
	return b
}

// factSubjects are the ids a remembered fact may be about: the user, the
// speaking character, and the other characters present.
func factSubjects(characterID string, participants []string) []string {
	out := []string{store.UserSpeaker, characterID}
	for _, id := range participants {
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
