// Package llm is the seam between the turn runner and a model provider.
// Types here are provider-neutral; adapters translate to their own wire format.
package llm

import (
	"context"
	"encoding/json"
)

// Message is one stored conversation message in the neutral format.
type Message struct {
	// Role is user, assistant, tool or trigger. Trigger is stored but sent as a
	// user message by the adapters.
	Role       string
	Content    string
	ToolCalls  []ToolCall // assistant messages only
	ToolCallID string     // tool messages only
	// Reasoning is echoed back within a tool loop only; it is not persisted.
	Reasoning string
}

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON, as the model produced it
}

type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON schema, passed through untouched
}

type Usage struct {
	InputTokens  int
	OutputTokens int
}

type Request struct {
	Model    string
	System   string
	Messages []Message
	Tools    []ToolSpec
	Session  string // provider session header; unused by Claude
}

type Response struct {
	Text      string // full text of this model response
	ToolCalls []ToolCall
	Reasoning string
	Usage     Usage
}

// Model is one model call, not the tool loop. Stream calls onText as text
// arrives (it may be called many times, or not at all) and returns the
// completed response.
type Model interface {
	Stream(ctx context.Context, req Request, onText func(string)) (Response, error)
}

// Provider identifies which adapter a character uses.
type Provider string

const (
	OpenCodeProvider Provider = "opencode"
	ClaudeProvider   Provider = "claude"
)
