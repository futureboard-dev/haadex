package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const EmbedDim = 3072

// Embedder generates vector embeddings via an embeddings API.
type Embedder struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

type openAIEmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type openAIEmbedBatchRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// NewEmbedder creates an Embedder that connects to an embeddings API using the given credentials.
func NewEmbedder(apiKey, baseURL, model string) *Embedder {
	return &Embedder{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

// Embed generates a vector for the given text.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	payload := openAIEmbedRequest{
		Model: e.model,
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

// EmbedBatch generates vectors for a slice of texts in a single API call.
// Retries up to 3 attempts total with exponential backoff (1s, 2s).
// On HTTP 429, the Retry-After header duration is used instead of the default backoff.
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	payload := openAIEmbedBatchRequest{
		Model: e.model,
		Input: texts,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	backoff := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	var lastErr error

	for attempt := 0; attempt < 3; attempt++ {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/v1/embeddings", bytes.NewReader(body))
		if reqErr != nil {
			return nil, fmt.Errorf("openai request: %w", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+e.apiKey)

		resp, doErr := e.client.Do(req)
		if doErr != nil {
			lastErr = fmt.Errorf("openai request: %w", doErr)
			if attempt < 2 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(backoff[attempt]):
				}
			}
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := resp.Header.Get("Retry-After")
			resp.Body.Close()
			lastErr = fmt.Errorf("openai rate limited (429)")
			if attempt < 2 {
				d := backoff[attempt]
				if n, parseErr := strconv.Atoi(retryAfter); parseErr == nil {
					d = time.Duration(n) * time.Second
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(d):
				}
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("openai status %d", resp.StatusCode)
			if attempt < 2 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(backoff[attempt]):
				}
			}
			continue
		}

		var result openAIEmbedResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if decodeErr != nil {
			lastErr = fmt.Errorf("decode openai response: %w", decodeErr)
			if attempt < 2 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(backoff[attempt]):
				}
			}
			continue
		}

		if len(result.Data) != len(texts) {
			return nil, fmt.Errorf("openai returned %d embeddings for %d inputs", len(result.Data), len(texts))
		}

		out := make([][]float32, len(texts))
		for i, d := range result.Data {
			out[i] = d.Embedding
		}
		return out, nil
	}
	return nil, lastErr
}
