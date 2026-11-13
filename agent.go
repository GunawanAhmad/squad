package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
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
var client = sync.OnceValue(func() *openai.Client {
	c := openai.NewClient(
		option.WithBaseURL(envOr("OPENCODE_BASE_URL", "https://opencode.ai/inference/go/openai/v1")),
		option.WithAPIKey(os.Getenv("OPENCODE_API_KEY")),
		option.WithHeader("User-Agent", "characters/0.1"),
	)
	return &c
})

func runChat(ctx context.Context, c Character, sessionID string, history []chatMsg, send func(event)) error {
	msgs := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(c.Persona)}
	for _, m := range history {
		if m.Role == "user" {
			msgs = append(msgs, openai.UserMessage(m.Content))
		} else {
			msgs = append(msgs, openai.AssistantMessage(m.Content))
		}
	}

	params := openai.ChatCompletionNewParams{Model: c.Model, Messages: msgs}
	for _, t := range c.Tools {
		params.Tools = append(params.Tools, openai.ChatCompletionFunctionTool(toolSpecs[t]))
	}

	for range maxSteps {
		stream := client().Chat.Completions.NewStreaming(ctx, params, option.WithHeader("x-opencode-session", sessionID))
		acc := openai.ChatCompletionAccumulator{}
		var reasoning strings.Builder
		for stream.Next() {
			chunk := stream.Current()
			if !acc.AddChunk(chunk) {
				err := stream.Close()
				if err != nil {
					return err
				}

				return errors.New("model stream sent an inconsistent chunk")
			}

			if len(chunk.Choices) == 0 {
				continue
			}

			d := chunk.Choices[0].Delta
			if d.Content != "" {
				send(event{Type: "text", Delta: d.Content})
			}

			// Reasoning models stream their thinking here; hidden from UI but sent back during tool loops.
			if f, ok := d.JSON.ExtraFields["reasoning_content"]; ok {
				var s string
				if json.Unmarshal([]byte(f.Raw()), &s) == nil {
					reasoning.WriteString(s)
				}
			}
		}

		err := stream.Err()
		if err != nil {
			return err
		}

		err = stream.Close()
		if err != nil {
			return err
		}

		if len(acc.Choices) == 0 {
			return errors.New("empty response from model")
		}

		msg := acc.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			return nil
		}

		asst := msg.ToAssistantMessageParam()

		if reasoning.Len() > 0 {
			asst.SetExtraFields(map[string]any{"reasoning_content": reasoning.String()})
		}

		params.Messages = append(params.Messages, openai.ChatCompletionMessageParamUnion{OfAssistant: &asst})
		for _, tc := range msg.ToolCalls {
			send(event{Type: "tool", Name: tc.Function.Name, Args: tc.Function.Arguments})
			params.Messages = append(params.Messages, openai.ToolMessage(runTool(ctx, c, tc.Function.Name, tc.Function.Arguments), tc.ID))
		}
	}
	send(event{Type: "text", Delta: "\n\n(stopped: too many tool steps)"})
	return nil
}

func transcript(history []chatMsg) string {
	if len(history) == 1 {
		return history[0].Content
	}

	var b strings.Builder
	b.WriteString("Conversation so far:\n\n")

	for _, m := range history[:len(history)-1] {
		role := "User"
		if m.Role == "assistant" {
			role = "You"
		}
		fmt.Fprintf(&b, "%s: %s\n\n", role, m.Content)
	}

	b.WriteString("Reply to the user's latest message:\n\n")
	b.WriteString(history[len(history)-1].Content)

	return b.String()
}

func runClaude(ctx context.Context, c Character, sessionID string, history []chatMsg, send func(event)) error {
	cmd := exec.CommandContext(ctx, "claude", "-p",
		"--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--system-prompt", c.Persona, // replaces Claude Code's own prompt
		"--model", c.Model,
		"--tools", "WebSearch,WebFetch", // nothing else: no shell, no file access
		"--allowedTools", "WebSearch,WebFetch", // no permission prompts in -p mode
		"--setting-sources", "project", // skip your ~/.claude hooks and plugins
		"--strict-mcp-config", // skip your MCP servers
		"--no-session-persistence",
	)
	tmpDir := os.TempDir()
	cmd.Dir = tmpDir
	cmd.Env = []string{}
	cmd.Stdin = strings.NewReader(transcript(history))
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	sc := bufio.NewScanner(out)
	if sc.Err() != nil {
		return sc.Err()
	}

	sc.Buffer(nil, 10<<20) // tool results can be long lines
	for sc.Scan() {
		var line struct {
			Type  string `json:"type"`
			Event struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			} `json:"event"`
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue // skip anything that isn't a JSON line
		}

		switch {
		case line.Type == "stream_event" && line.Event.Delta.Type == "text_delta":
			send(event{Type: "text", Delta: line.Event.Delta.Text})
		case line.Type == "result" && line.IsError:
			send(event{Type: "error", Message: line.Result})
		}
	}
	return cmd.Wait()
}
