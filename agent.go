package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"characters/llm"
)

const maxSteps = 10

type event struct {
	Type    string `json:"type"` // text | tool | error
	Delta   string `json:"delta,omitempty"`
	Name    string `json:"name,omitempty"`
	Args    string `json:"args,omitempty"`
	Message string `json:"message,omitempty"`
}

// Created lazily so .env is loaded first.
var openCodeModel = sync.OnceValue(func() llm.Model {
	return llm.NewOpenCode(
		os.Getenv("OPENCODE_API_KEY"),
		envOr("OPENCODE_BASE_URL", "https://opencode.ai/inference/go/openai/v1"),
	)
})

var claudeModel = sync.OnceValue(func() llm.Model { return llm.NewClaude() })

func loadSystemPrompt(c Character) string {
	memory := memoryPrompt(c)
	sysPrompt := c.Persona

	// Can we not trim everything before send this?
	if strings.TrimSpace(memory) != "" {
		sysPrompt = c.Persona + "\n\n" + memory
	}

	return sysPrompt + nowBlock(time.Now(), timeZone())
}

// Tool specs are mapped here until the typed registry lands (F-6).
func specsFor(c Character) []llm.ToolSpec {
	var out []llm.ToolSpec
	for _, name := range c.Tools {
		spec, ok := toolSpecs[name]
		if !ok {
			continue
		}
		params, _ := json.Marshal(spec.Parameters)
		out = append(out, llm.ToolSpec{Name: name, Description: spec.Description.Value, Parameters: params})
	}
	return out
}

// runChat is the old /api/chat loop. F-7 moves it to assistant.Turn and F-9
// deletes this path; until then it keeps characters working on the new adapters.
func runChat(ctx context.Context, c Character, sessionID string, history []chatMsg, send func(event)) error {
	msgs := make([]llm.Message, 0, len(history))
	for _, m := range history {
		msgs = append(msgs, llm.Message{Role: m.Role, Content: m.Content})
	}

	system := loadSystemPrompt(c)
	specs := specsFor(c)
	for range maxSteps {
		resp, err := openCodeModel().Stream(ctx, llm.Request{
			Model: c.Model, System: system, Messages: msgs, Tools: specs, Session: sessionID,
		}, func(text string) {
			send(event{Type: "text", Delta: text})
		})
		if err != nil {
			return err
		}
		if len(resp.ToolCalls) == 0 {
			return nil
		}

		msgs = append(msgs, llm.Message{
			Role: "assistant", Content: resp.Text, ToolCalls: resp.ToolCalls, Reasoning: resp.Reasoning,
		})
		for _, tc := range resp.ToolCalls {
			send(event{Type: "tool", Name: tc.Name, Args: tc.Arguments})
			msgs = append(msgs, llm.Message{
				Role: "tool", ToolCallID: tc.ID, Content: runTool(ctx, c, tc.Name, tc.Arguments),
			})
		}
	}
	send(event{Type: "text", Delta: "\n\n(stopped: too many tool steps)"})
	return nil
}

func runClaude(ctx context.Context, c Character, _ string, history []chatMsg, send func(event)) error {
	msgs := make([]llm.Message, 0, len(history))
	for _, m := range history {
		msgs = append(msgs, llm.Message{Role: m.Role, Content: m.Content})
	}

	_, err := claudeModel().Stream(ctx, llm.Request{
		Model: c.Model, System: loadSystemPrompt(c), Messages: msgs,
	}, func(text string) {
		send(event{Type: "text", Delta: text})
	})
	return err
}
