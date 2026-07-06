package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mockOpenAIServer(t *testing.T, embedding []float32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIEmbedResponse{
			Data: []struct {
				Embedding []float32 `json:"embedding"`
			}{{Embedding: embedding}},
		})
	}))
}

func newTestEmbedder(baseURL string) *Embedder {
	return &Embedder{apiKey: "test", baseURL: baseURL, client: &http.Client{}}
}

func TestEmbed_Success(t *testing.T) {
	want := []float32{0.1, 0.2, 0.3}
	srv := mockOpenAIServer(t, want)
	defer srv.Close()

	got, err := newTestEmbedder(srv.URL).Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d dims, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dim %d: expected %f, got %f", i, want[i], got[i])
		}
	}
}

func TestEmbed_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newTestEmbedder(srv.URL).Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
}

func TestEmbed_EmptyEmbedding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIEmbedResponse{})
	}))
	defer srv.Close()

	_, err := newTestEmbedder(srv.URL).Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for empty embedding")
	}
}

func TestEmbed_ConnectionRefused(t *testing.T) {
	_, err := newTestEmbedder("http://127.0.0.1:19999").Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error when server is unreachable")
	}
}

func TestEmbed_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	_, err := newTestEmbedder(srv.URL).Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func mockBatchServer(t *testing.T, embeddings [][]float32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		data := make([]struct {
			Embedding []float32 `json:"embedding"`
		}, len(embeddings))
		for i, e := range embeddings {
			data[i].Embedding = e
		}
		json.NewEncoder(w).Encode(openAIEmbedResponse{Data: data})
	}))
}

func TestEmbedBatch_Success(t *testing.T) {
	want := [][]float32{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}}
	srv := mockBatchServer(t, want)
	defer srv.Close()

	got, err := newTestEmbedder(srv.URL).EmbedBatch(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d embeddings, got %d", len(want), len(got))
	}
	for i := range want {
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Errorf("embedding[%d][%d]: expected %f, got %f", i, j, want[i][j], got[i][j])
			}
		}
	}
}

func TestEmbedBatch_PositionalOrder(t *testing.T) {
	// Embeddings returned in order matching input texts.
	want := [][]float32{{1.0}, {2.0}, {3.0}}
	srv := mockBatchServer(t, want)
	defer srv.Close()

	got, err := newTestEmbedder(srv.URL).EmbedBatch(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	for i, w := range want {
		if got[i][0] != w[0] {
			t.Errorf("position %d: expected %f, got %f", i, w[0], got[i][0])
		}
	}
}

func TestEmbedBatch_RetryOn429(t *testing.T) {
	want := [][]float32{{1.0, 2.0}, {3.0, 4.0}}
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		data := make([]struct {
			Embedding []float32 `json:"embedding"`
		}, len(want))
		for i, e := range want {
			data[i].Embedding = e
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIEmbedResponse{Data: data})
	}))
	defer srv.Close()

	got, err := newTestEmbedder(srv.URL).EmbedBatch(context.Background(), []string{"x", "y"})
	if err != nil {
		t.Fatalf("EmbedBatch after retry: %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
	for i, w := range want {
		for j := range w {
			if got[i][j] != w[j] {
				t.Errorf("embedding[%d][%d]: expected %f, got %f", i, j, w[j], got[i][j])
			}
		}
	}
}

func TestEmbedBatch_AllRetriesExhausted429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := newTestEmbedder(srv.URL).EmbedBatch(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("expected error when all retries exhausted")
	}
}

func TestEmbedBatch_LengthMismatch(t *testing.T) {
	// Server returns 1 embedding but we requested 2 — should error.
	srv := mockBatchServer(t, [][]float32{{1.0}})
	defer srv.Close()

	_, err := newTestEmbedder(srv.URL).EmbedBatch(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error for length mismatch")
	}
}

func TestEmbedBatch_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newTestEmbedder(srv.URL).EmbedBatch(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
}
