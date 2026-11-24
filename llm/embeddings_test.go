package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIEmbeddings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/embeddings" {
			t.Errorf("path: %s", r.URL.Path)
		}
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req.Model != "bge-m3" || len(req.Input) != 2 {
			t.Errorf("request: %+v", req)
		}
		// Out of order on purpose: the client must sort by index.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "bge-m3",
			"data": []map[string]any{
				{"index": 1, "embedding": []float64{0, 1, 0}},
				{"index": 0, "embedding": []float64{1, 0, 0}},
			},
		})
	}))
	defer srv.Close()

	e := NewOpenAIEmbeddings(srv.URL, "ollama", "bge-m3", 3)
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Fatalf("vectors not sorted by index: %+v", vecs)
	}
	if e.Model() != "bge-m3" || e.Dims() != 3 {
		t.Fatalf("metadata: %s %d", e.Model(), e.Dims())
	}

	// Wrong dimensions are an error, never silently accepted.
	bad := NewOpenAIEmbeddings(srv.URL, "ollama", "bge-m3", 1024)
	if _, err := bad.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("dimension mismatch accepted")
	}
}
