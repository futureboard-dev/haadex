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
