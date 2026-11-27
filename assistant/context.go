package assistant

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"squad/llm"
	"squad/store"
)

const DefaultWindow = 40

// Context v2 budgets. They sum below MaxSystemChars, so the total is a safety
// net, not the normal case; every cut is recorded and logged.
const (
	MaxSystemChars  = 16000
	budgetPersona   = 4000
	budgetMemory    = 3000
	budgetToday     = 3000
	budgetDigest    = 2000
	budgetRecall    = 1500
	budgetRoom      = 400
	budgetSummary   = 1200
	truncatedSuffix = "\n…(truncated)\n"
)

// Character is the part of a character config the turn runner needs.
type Character struct {
	ID       string
	Name     string
	Provider string
	Model    string
	Persona  string
	Tools    []string
	// Context is "memory" (profile.md + the character's own facts and
	// pre-retrieval) or "none". Every agent has its own memory; none is shared.
	Context string
	// Daily marks the assistant: it gets the day's records in context and
	// lives in one thread per day. Personas never see the user's day.
	Daily bool
	// Discretion (0-10) is how much the character shares about the user in a
	// room; the share gate uses it.
	Discretion int
}

// hasMemory reports whether the character's context setting turns on the memory
// stack (profile, own facts, own retrieval).
func (c Character) hasMemory() bool { return c.Context != "none" }

// Context is the prompt for one turn: the system text and the recent history
// that fits the window. BuildContext is pure so golden tests can pin it.
type Context struct {
	System     string
	Messages   []llm.Message
	MessageIDs []int64
	Sections   []string
	Truncated  []string // sections that hit their budget
	Window     int
}

// ContextInput is everything the prompt builder needs. A struct keeps later
// phases (facts, retrieved hits, summary) from turning the signature into a
// row of booleans.
type ContextInput struct {
	Character Character
	Memory    string // profile + core facts block
	History   []store.Message
	Window    int
	Now       time.Time
	Settings  Settings
	Records   *Records    // nil for persona chats
	Retrieved []Retrieved // pre-retrieval hits, quoted as data
	Summary   string      // rolling summary of older messages
	// Names maps a speaker id to its display name ("me" is "the user"); it
	// labels room history. Empty in 1:1 chats.
	Names map[string]string
	// Listeners are the other characters present. Non-empty means a room turn,
	// which tells the character how handing the turn by @mention works.
	Listeners []string
}

// BuildContext assembles persona, memory, the day's records, the clock and the
// last window messages. The window is cut at a user turn so tool call/result
// pairs stay together.
func BuildContext(in ContextInput) Context {
	char, memory, history, now, set, rec := in.Character, in.Memory, in.History, in.Now, in.Settings, in.Records
	window := in.Window
	if window <= 0 {
		window = DefaultWindow
	}
	loc := set.Loc
	if loc == nil {
		loc = time.UTC
	}

	var sections, truncated []string
	fit := func(name, text string, budget int) string {
		if len(text) <= budget {
			return text
		}
		truncated = append(truncated, name)
		return clipRunes(text, budget) + truncatedSuffix
	}

	system := fit("persona", strings.TrimSpace(char.Persona), budgetPersona)
	sections = append(sections, "persona")
	if strings.TrimSpace(memory) != "" {
		system += fit("memory", "\n\n"+memory, budgetMemory)
		sections = append(sections, "memory")
	}
	if len(in.Listeners) > 0 {
		system += fit("room", "\n\n<room>\nYou are in a room with "+strings.Join(in.Listeners, ", ")+
			". Everyone here reads your reply. To hand the turn to another member, @mention them by name; a reply with no mention (or only yourself) ends the conversation after you.\n</room>\n", budgetRoom)
		sections = append(sections, "room")
	}
	if rec != nil {
		today, digest := rec.PromptParts(loc, set.DayStartsAt, now)
		system += fit("today", today, budgetToday)
		sections = append(sections, "today")
		if digest != "" {
			system += fit("recent_days", digest, budgetDigest)
			sections = append(sections, "recent_days")
		}
	}
	if len(in.Retrieved) > 0 {
		system += fit("recall", recallPrompt(in.Retrieved), budgetRecall)
		sections = append(sections, "recall")
	}
	if strings.TrimSpace(in.Summary) != "" {
		system += fit("summary", "\n<summary>\nEarlier in this conversation (compressed):\n"+
			strings.TrimSpace(in.Summary)+"\n</summary>\n", budgetSummary)
		sections = append(sections, "summary")
	}
	system += NowBlock(now, loc)
	sections = append(sections, "now")
	if len(system) > MaxSystemChars {
		truncated = append(truncated, "total")
		system = clipRunes(system, MaxSystemChars) + truncatedSuffix
	}

	if len(history) > window {
		history = history[len(history)-window:]
	}
	for len(history) > 0 && history[0].Role != "user" && history[0].Role != "trigger" {
		history = history[1:]
	}
	if len(history) > 0 {
		sections = append(sections, "history")
	}

	c := Context{System: system, Sections: sections, Truncated: truncated, Window: window}
	for _, t := range truncated {
		log.Printf("context: %s section truncated to its budget", t)
	}
	for _, m := range history {
		c.MessageIDs = append(c.MessageIDs, m.ID)
		c.Messages = append(c.Messages, toLLMMessage(m, in.Names))
	}
	return c
}

// clipRunes cuts at a rune boundary so a truncation never produces invalid UTF-8.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func toLLMMessage(m store.Message, names map[string]string) llm.Message {
	out := llm.Message{Role: m.Role, Content: speakerLine(m.Role, m.Speaker, m.Content, names), ToolCallID: m.ToolCallID}
	if len(m.ToolCalls) > 0 && string(m.ToolCalls) != "null" {
		_ = json.Unmarshal(m.ToolCalls, &out.ToolCalls)
	}
	return out
}

// speakerLine prefixes a room message with who said it. Content is stored
// without the label, so nothing is ever labeled twice; an unknown id falls back
// to the id itself. 1:1 messages have no speaker and stay unchanged.
func speakerLine(role, speaker, content string, names map[string]string) string {
	if role != "user" || speaker == "" {
		return content
	}
	name := names[speaker]
	if name == "" {
		name = speaker
	}
	return "[" + name + "]: " + content
}
