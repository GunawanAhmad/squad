package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// OpenCode talks to the OpenCode gateway through its OpenAI-compatible API.
type OpenCode struct {
	Client *openai.Client
}

func NewOpenCode(apiKey, baseURL string) *OpenCode {
	c := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
		option.WithHeader("User-Agent", "characters/0.1"),
	)
	return &OpenCode{Client: &c}
}

func (o *OpenCode) Stream(ctx context.Context, req Request, onText func(string)) (Response, error) {
	params := openai.ChatCompletionNewParams{
		Model:    req.Model,
		Messages: openCodeMessages(req.System, req.Messages),
	}
	for _, t := range req.Tools {
		var paramsJSON shared.FunctionParameters
		if len(t.Parameters) > 0 {
			if err := json.Unmarshal(t.Parameters, &paramsJSON); err != nil {
				return Response{}, err
			}
		}
		params.Tools = append(params.Tools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  paramsJSON,
		}))
	}

	stream := o.Client.Chat.Completions.NewStreaming(ctx, params, option.WithHeader("x-opencode-session", req.Session))
	acc := openai.ChatCompletionAccumulator{}
	var reasoning strings.Builder
	for stream.Next() {
		chunk := stream.Current()
		if !acc.AddChunk(chunk) {
			_ = stream.Close()
			return Response{}, errors.New("model stream sent an inconsistent chunk")
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		if d.Content != "" {
			onText(d.Content)
		}
		// Reasoning models stream their thinking here; hidden from UI but sent back during tool loops.
		if f, ok := d.JSON.ExtraFields["reasoning_content"]; ok {
			var s string
			if json.Unmarshal([]byte(f.Raw()), &s) == nil {
				reasoning.WriteString(s)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return Response{}, err
	}
	if err := stream.Close(); err != nil {
		return Response{}, err
	}
	if len(acc.Choices) == 0 {
		return Response{}, errors.New("empty response from model")
	}

	msg := acc.Choices[0].Message
	resp := Response{
		Text:      msg.Content,
		Reasoning: reasoning.String(),
		Usage: Usage{
			InputTokens:  int(acc.Usage.PromptTokens),
			OutputTokens: int(acc.Usage.CompletionTokens),
		},
	}
	for _, tc := range msg.ToolCalls {
		if tc.Function.Name == "" {
			continue
		}
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return resp, nil
}

func openCodeMessages(system string, msgs []Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs)+1)
	out = append(out, openai.SystemMessage(system))
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			asst := openai.ChatCompletionAssistantMessageParam{}
			if m.Content != "" || len(m.ToolCalls) == 0 {
				asst.Content = openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(m.Content)}
			}
			for _, tc := range m.ToolCalls {
				if tc.ID == "" {
					continue // an unfinished tool call must not be replayed
				}
				asst.ToolCalls = append(asst.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
						ID: tc.ID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name:      tc.Name,
							Arguments: tc.Arguments,
						},
					},
				})
			}
			if m.Reasoning != "" {
				asst.SetExtraFields(map[string]any{"reasoning_content": m.Reasoning})
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &asst})
		case "tool":
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))
		default: // user, trigger
			out = append(out, openai.UserMessage(m.Content))
		}
	}
	return out
}
