package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"squad/llm"
)

// Structured asks a model for exactly one JSON object and parses it. There is
// no response_format on the gateway route, so the schema is described in the
// prompt and the parser is strict: markdown fences and prose are tolerated, but
// the object itself must be valid JSON. The caller validates fields item by item
// (Rejects) and logs what it refused; nothing invalid is ever applied.

type StructuredSpec struct {
	Model   llm.Model
	ModelID string
	System  string
	Prompt  string
	Schema  string // JSON schema or an example object, rendered into the prompt
	Session string
}

// CallStructured returns the model's JSON object.
func CallStructured(ctx context.Context, spec StructuredSpec) (json.RawMessage, llm.Usage, error) {
	if spec.Model == nil {
		return nil, llm.Usage{}, fmt.Errorf("structured: no model")
	}
	prompt := strings.TrimSpace(spec.Prompt)
	if strings.TrimSpace(spec.Schema) != "" {
		prompt += "\n\nReply with exactly one JSON object with this shape (no prose, no markdown):\n" + spec.Schema
	}
	resp, err := spec.Model.Stream(ctx, llm.Request{
		Model: spec.ModelID, System: spec.System,
		Messages: []llm.Message{{Role: "user", Content: prompt}},
		Session:  spec.Session,
	}, func(string) {})
	if err != nil {
		return nil, resp.Usage, err
	}
	raw, err := ExtractJSONObject(resp.Text)
	if err != nil {
		return nil, resp.Usage, fmt.Errorf("structured: %w", err)
	}
	return raw, resp.Usage, nil
}

// ExtractJSONObject finds the first JSON object in a model reply, skipping
// markdown fences and any prose before or after it.
func ExtractJSONObject(text string) (json.RawMessage, error) {
	start := strings.Index(text, "{")
	if start < 0 {
		return nil, fmt.Errorf("reply contains no JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(text[start:])))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("reply is not valid JSON: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("reply is not a JSON object: %w", err)
	}
	return raw, nil
}

// Rejects collects per-item validation failures. A job logs them on its run;
// it never applies them.
type Rejects []string

func (r *Rejects) Add(format string, args ...any) {
	*r = append(*r, fmt.Sprintf(format, args...))
}

// JSON is what goes into runs.context (nil when nothing was rejected).
func (r Rejects) JSON() json.RawMessage {
	if len(r) == 0 {
		return nil
	}
	raw, _ := json.Marshal(map[string]any{"rejected": []string(r)})
	return raw
}
