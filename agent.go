package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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
			return nil
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
