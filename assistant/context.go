package assistant

import (
	"encoding/json"
	"strings"
	"time"

	"characters/llm"
	"characters/store"
)

const DefaultWindow = 40

// Character is the part of a character config the turn runner needs.
type Character struct {
	ID       string
	Name     string
	Provider string
	Model    string
	Persona  string
	Tools    []string
}

// Context is the prompt for one turn: the system text and the recent history
// that fits the window. BuildContext is pure so golden tests can pin it.
type Context struct {
	System     string
	Messages   []llm.Message
	MessageIDs []int64
	Sections   []string
	Window     int
}

// BuildContext assembles persona, memory, clock and the last window messages.
// The window is cut at a user turn so tool call/result pairs stay together.
func BuildContext(char Character, memory string, now time.Time, loc *time.Location, history []store.Message, window int) Context {
	if window <= 0 {
		window = DefaultWindow
	}

	var sections []string
	system := strings.TrimSpace(char.Persona)
	sections = append(sections, "persona")
	if strings.TrimSpace(memory) != "" {
		system += "\n\n" + memory
		sections = append(sections, "memory")
	}
	system += NowBlock(now, loc)
	sections = append(sections, "now")

	if len(history) > window {
		history = history[len(history)-window:]
	}
	for len(history) > 0 && history[0].Role != "user" && history[0].Role != "trigger" {
		history = history[1:]
	}
	if len(history) > 0 {
		sections = append(sections, "history")
	}

	c := Context{System: system, Sections: sections, Window: window}
	for _, m := range history {
		c.MessageIDs = append(c.MessageIDs, m.ID)
		c.Messages = append(c.Messages, toLLMMessage(m))
	}
	return c
}

func toLLMMessage(m store.Message) llm.Message {
	out := llm.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
	if len(m.ToolCalls) > 0 && string(m.ToolCalls) != "null" {
		_ = json.Unmarshal(m.ToolCalls, &out.ToolCalls)
	}
	return out
}
