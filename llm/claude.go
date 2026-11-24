package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Claude runs the `claude -p` CLI. It cannot call app tools: the CLI gets its
// own WebSearch/WebFetch and does them inside the subprocess.
type Claude struct {
	Bin string
}

func NewClaude() *Claude { return &Claude{Bin: "claude"} }

func (c *Claude) Stream(ctx context.Context, req Request, onText func(string)) (Response, error) {
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.CommandContext(ctx, bin, "-p",
		"--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--system-prompt", req.System, // replaces Claude Code's own prompt
		"--model", req.Model,
		"--tools", "WebSearch,WebFetch", // nothing else: no shell, no file access
		"--allowedTools", "WebSearch,WebFetch", // no permission prompts in -p mode
		"--setting-sources", "project", // skip your ~/.claude hooks and plugins
		"--strict-mcp-config", // skip your MCP servers
		"--no-session-persistence",
	)

	cmd.Dir = os.TempDir()
	cmd.Env = []string{}
	cmd.Stdin = strings.NewReader(claudeTranscript(req.Messages))
	out, err := cmd.StdoutPipe()
	if err != nil {
		return Response{}, err
	}
	if err := cmd.Start(); err != nil {
		return Response{}, err
	}

	sc := bufio.NewScanner(out)
	sc.Buffer(nil, 10<<20) // tool results can be long lines

	var text strings.Builder
	var usage Usage
	var runErr error
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
			Usage   struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue // skip anything that isn't a JSON line
		}

		switch {
		case line.Type == "stream_event" && line.Event.Delta.Type == "text_delta":
			text.WriteString(line.Event.Delta.Text)
			onText(line.Event.Delta.Text)
		case line.Type == "result":
			usage = Usage{InputTokens: line.Usage.InputTokens, OutputTokens: line.Usage.OutputTokens}
			if line.IsError {
				runErr = errors.New(line.Result)
			}
		}
	}
	scanErr := sc.Err()
	waitErr := cmd.Wait()
	if scanErr != nil {
		return Response{Text: text.String()}, scanErr
	}
	if runErr != nil {
		return Response{Text: text.String(), Usage: usage}, runErr
	}
	if waitErr != nil && ctx.Err() == nil {
		return Response{Text: text.String(), Usage: usage}, fmt.Errorf("claude: %w", waitErr)
	}
	return Response{Text: text.String(), Usage: usage}, waitErr
}

// claudeTranscript flattens history for stdin. The Claude CLI route has no app
// tools, so only user and assistant text is sent.
func claudeTranscript(msgs []Message) string {
	var conv []Message
	for _, m := range msgs {
		if m.Role == "user" || m.Role == "assistant" || m.Role == "trigger" {
			conv = append(conv, m)
		}
	}
	if len(conv) == 0 {
		return ""
	}
	if len(conv) == 1 {
		return conv[0].Content
	}

	var b strings.Builder
	b.WriteString("<conversation>\n\n")
	b.WriteString("Conversation so far:\n\n")
	for _, m := range conv[:len(conv)-1] {
		role := "User"
		if m.Role != "user" {
			role = "You"
		}
		fmt.Fprintf(&b, "%s: %s\n\n", role, m.Content)
	}
	b.WriteString("Reply to the user's latest message:\n\n")
	b.WriteString(conv[len(conv)-1].Content)
	b.WriteString("\n\n</conversation>\n")
	return b.String()
}
