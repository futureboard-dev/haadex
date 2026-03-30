package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Embedder generates vector embeddings via the Ollama REST API.
type Embedder struct {
	baseURL string
	model   string
	client  *http.Client
}

type ollamaEmbedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbedResponse struct {
	Embedding []float32 `json:"embedding"`
}

// NewEmbedder creates an Embedder that connects to the given Ollama base URL.
func NewEmbedder(baseURL string) *Embedder {
	return &Embedder{
		baseURL: baseURL,
		model:   "nomic-embed-text",
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// Embed generates a vector for the given text.
// Caller is responsible for prepending "search_document: " or "search_query: ".
func (e *Embedder) Embed(text string) ([]float32, error) {
	payload := ollamaEmbedRequest{
		Model:  e.model,
		Prompt: text,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := e.client.Post(e.baseURL+"/api/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama status %d", resp.StatusCode)
	}

	var result ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode ollama response: %w", err)
	}
	if len(result.Embedding) == 0 {
		return nil, fmt.Errorf("ollama returned empty embedding")
	}
	return result.Embedding, nil
}
