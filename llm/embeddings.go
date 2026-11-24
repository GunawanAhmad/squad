package llm

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// Embedder turns text into vectors. The app's implementation talks to a local
// Ollama through its OpenAI-compatible /v1/embeddings endpoint, so the text
// never leaves the machine. Tests use a fake.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Model() string
	Dims() int
}

// OpenAIEmbeddings is an OpenAI-compatible embeddings client (Ollama, or any
// gateway that speaks the same shape).
type OpenAIEmbeddings struct {
	client *openai.Client
	model  string
	dims   int
}

func NewOpenAIEmbeddings(baseURL, apiKey, model string, dims int) *OpenAIEmbeddings {
	c := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
		option.WithHeader("User-Agent", "characters/0.1"),
	)
	return &OpenAIEmbeddings{client: &c, model: model, dims: dims}
}

func (e *OpenAIEmbeddings) Model() string { return e.model }
func (e *OpenAIEmbeddings) Dims() int     { return e.dims }

func (e *OpenAIEmbeddings) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	resp, err := e.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(e.model),
		Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts},
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d texts", len(resp.Data), len(texts))
	}
	data := append([]openai.Embedding(nil), resp.Data...)
	sort.Slice(data, func(i, j int) bool { return data[i].Index < data[j].Index })

	out := make([][]float32, 0, len(data))
	for _, d := range data {
		if e.dims > 0 && len(d.Embedding) != e.dims {
			return nil, fmt.Errorf("embeddings: %s returned %d dimensions, want %d", e.model, len(d.Embedding), e.dims)
		}
		vec := make([]float32, len(d.Embedding))
		for i, v := range d.Embedding {
			vec[i] = float32(v)
		}
		out = append(out, vec)
	}
	return out, nil
}

// EmbedOne is the query path: one text, with the caller's timeout.
func EmbedOne(ctx context.Context, e Embedder, text string) ([]float32, error) {
	if e == nil {
		return nil, errors.New("no embedder")
	}
	vecs, err := e.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("embeddings: %d vectors for one text", len(vecs))
	}
	return vecs[0], nil
}
