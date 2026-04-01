package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const EmbedDim = 3072

// Embedder generates vector embeddings via the OpenAI embeddings API.
type Embedder struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

type openAIEmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// NewEmbedder creates an Embedder that connects to OpenAI using the given API key.
func NewEmbedder(apiKey string) *Embedder {
	return &Embedder{
		apiKey:  apiKey,
		baseURL: "https://api.openai.com",
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Embed generates a vector for the given text.
// Caller is responsible for prepending "search_document: " or "search_query: ".
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	payload := openAIEmbedRequest{
		Model: "text-embedding-3-large",
		Input: text,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai status %d", resp.StatusCode)
	}

	var result openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode openai response: %w", err)
	}
	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("openai returned empty embedding")
	}
	return result.Data[0].Embedding, nil
}
