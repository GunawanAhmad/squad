package llm

import (
	"context"
	"fmt"
	"sync"
)

// Fake is a scripted Model for tests. It is intentionally in the normal
// package so the store/assistant tests can use it without a build tag.
type FakeStep struct {
	Text      string
	ToolCalls []ToolCall
	Reasoning string
	Usage     Usage
	Err       error
	// Block waits for the context to end before returning its Err, to test
	// cancellation and to keep a turn in flight for lock tests.
	Block bool
}

type Fake struct {
	mu    sync.Mutex
	steps []FakeStep
	reqs  []Request
	// active/maxActive detect overlapping model calls (two turns on one thread).
	active    int
	maxActive int
}

// NewFake scripts the responses that will be returned in order.
func NewFake(steps ...FakeStep) *Fake { return &Fake{steps: steps} }

func (f *Fake) Stream(ctx context.Context, req Request, onText func(string)) (Response, error) {
	f.mu.Lock()
	if len(f.steps) == 0 {
		f.mu.Unlock()
		return Response{}, fmt.Errorf("fake: no scripted response for %q", req.Model)
	}
	step := f.steps[0]
	f.steps = f.steps[1:]
	f.reqs = append(f.reqs, req)
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()

	if step.Block {
		<-ctx.Done()
		return Response{}, ctx.Err()
	}
	if step.Text != "" {
		onText(step.Text)
	}
	if step.Err != nil {
		return Response{}, step.Err
	}
	return Response{Text: step.Text, ToolCalls: step.ToolCalls, Reasoning: step.Reasoning, Usage: step.Usage}, nil
}

// Requests returns a copy of the requests seen so far.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.reqs...)
}

// MaxActive is the largest number of overlapping Stream calls seen.
func (f *Fake) MaxActive() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}

// Remaining is how many scripted steps are left.
func (f *Fake) Remaining() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.steps)
}
