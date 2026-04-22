package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	enrichBaseURL   = "https://openrouter.ai/api/v1"
	enrichModel     = "deepseek/deepseek-chat-v3-0324"
	maxContentChars = 6000
)

// Summarizer calls OpenRouter chat completions to generate contextual descriptions.
type Summarizer struct {
	apiKey    string
	baseURL   string
	client    *http.Client
	retryWait time.Duration // per-attempt backoff multiplier; defaults to 2s
}

// ChunkContext pairs a symbol name with its LLM-generated description.
type ChunkContext struct {
	Name    string `json:"name"`
	Context string `json:"context"`
}

// FileEnrichment holds the LLM output for a single file.
type FileEnrichment struct {
	FileSummary   string
	ChunkContexts []ChunkContext
}

// NewSummarizer creates a Summarizer using the given OpenRouter API key.
func NewSummarizer(apiKey string) *Summarizer {
	return &Summarizer{
		apiKey:    apiKey,
		baseURL:   enrichBaseURL,
		client:    &http.Client{Timeout: 60 * time.Second},
		retryWait: 2 * time.Second,
	}
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	ResponseFormat *chatFormat   `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type enrichJSON struct {
	FileSummary string `json:"file_summary"`
	Symbols     []struct {
		Name    string `json:"name"`
		Context string `json:"context"`
	} `json:"symbols"`
}

// EnrichFile sends one LLM call per file and returns descriptions for all chunks + a file summary.
// If the call fails after retries, it returns an error; callers should fall back gracefully.
func (s *Summarizer) EnrichFile(ctx context.Context, filePath string, content string, chunks []Chunk) (*FileEnrichment, error) {
	if len(content) > maxContentChars {
		content = content[:maxContentChars]
	}

	lang := extLang(filePath)

	symbolParts := make([]string, len(chunks))
	for i, c := range chunks {
		symbolParts[i] = fmt.Sprintf("%s (%s)", c.Name, c.Kind)
	}

	prompt := fmt.Sprintf(
		"You are a code indexer. Given a source file and its symbols, produce:\n"+
			"1. A 2-3 sentence summary of the file's purpose.\n"+
			"2. For each symbol, a 1-2 sentence description of what it does and its role in the file.\n\n"+
			"File: %s\n"+
			"Symbols: %s\n\n"+
			"```%s\n%s\n```\n\n"+
			"Respond with JSON:\n"+
			"{\n"+
			"  \"file_summary\": \"...\",\n"+
			"  \"symbols\": [{\"name\": \"...\", \"context\": \"...\"}, ...]\n"+
			"}",
		filePath,
		strings.Join(symbolParts, ", "),
		lang,
		content,
	)

	req := chatRequest{
		Model:          enrichModel,
		Messages:       []chatMessage{{Role: "user", Content: prompt}},
		ResponseFormat: &chatFormat{Type: "json_object"},
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * s.retryWait):
			}
		}
		result, err := s.callAPI(ctx, req)
		if err == nil {
			return result, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("enrich %s after 3 attempts: %w", filePath, lastErr)
}

func (s *Summarizer) callAPI(ctx context.Context, req chatRequest) (*FileEnrichment, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var chat chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chat); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(chat.Choices) == 0 || chat.Choices[0].Message.Content == "" {
		return nil, fmt.Errorf("empty response")
	}

	var parsed enrichJSON
	if err := json.Unmarshal([]byte(chat.Choices[0].Message.Content), &parsed); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	enrichment := &FileEnrichment{
		FileSummary:   parsed.FileSummary,
		ChunkContexts: make([]ChunkContext, 0, len(parsed.Symbols)),
	}
	for _, sym := range parsed.Symbols {
		enrichment.ChunkContexts = append(enrichment.ChunkContexts, ChunkContext{
			Name:    sym.Name,
			Context: sym.Context,
		})
	}
	return enrichment, nil
}

// extLang maps a file path to a code-fence language hint.
func extLang(filePath string) string {
	i := strings.LastIndex(filePath, ".")
	if i < 0 {
		return ""
	}
	switch strings.ToLower(filePath[i:]) {
	case ".go":
		return "go"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".js", ".jsx":
		return "javascript"
	case ".py":
		return "python"
	default:
		return ""
	}
}
