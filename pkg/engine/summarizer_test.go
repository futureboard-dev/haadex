package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestSummarizer creates a Summarizer pointed at the given base URL with no retry delay.
func newTestSummarizer(baseURL string) *Summarizer {
	return &Summarizer{apiKey: "test-key", baseURL: baseURL, client: &http.Client{}, retryWait: 0}
}

// mockEnrichServer returns a test server that responds with the given file summary and symbols.
func mockEnrichServer(t *testing.T, fileSummary string, symbols []ChunkContext) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		inner := enrichJSON{FileSummary: fileSummary}
		for _, s := range symbols {
			inner.Symbols = append(inner.Symbols, struct {
				Name    string `json:"name"`
				Context string `json:"context"`
			}{Name: s.Name, Context: s.Context})
		}
		innerBytes, _ := json.Marshal(inner)
		outer := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{{Message: struct {
				Content string `json:"content"`
			}{Content: string(innerBytes)}}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(outer)
	}))
}

func TestEnrichFile_Success(t *testing.T) {
	wantSummary := "This file handles user authentication."
	wantSymbols := []ChunkContext{
		{Name: "handleLogin", Context: "Handles login form submission."},
		{Name: "handleLogout", Context: "Clears the session on logout."},
	}

	srv := mockEnrichServer(t, wantSummary, wantSymbols)
	defer srv.Close()

	chunks := []Chunk{
		{Name: "handleLogin", Kind: "function"},
		{Name: "handleLogout", Kind: "function"},
	}

	result, err := newTestSummarizer(srv.URL).EnrichFile(context.Background(), "auth.ts", "const x = 1;", chunks)
	if err != nil {
		t.Fatalf("EnrichFile: %v", err)
	}
	if result.FileSummary != wantSummary {
		t.Errorf("FileSummary: got %q, want %q", result.FileSummary, wantSummary)
	}
	if len(result.ChunkContexts) != len(wantSymbols) {
		t.Fatalf("ChunkContexts len: got %d, want %d", len(result.ChunkContexts), len(wantSymbols))
	}
	for i, want := range wantSymbols {
		got := result.ChunkContexts[i]
		if got.Name != want.Name || got.Context != want.Context {
			t.Errorf("ChunkContexts[%d]: got {%q,%q}, want {%q,%q}", i, got.Name, got.Context, want.Name, want.Context)
		}
	}
}

func TestEnrichFile_NameMatching(t *testing.T) {
	// The server returns contexts keyed by name — callers must match on name to attach.
	srv := mockEnrichServer(t, "A settings page.", []ChunkContext{
		{Name: "handleSubmit", Context: "Submits the form."},
		{Name: "UserSettingsPage", Context: "Root component for user settings."},
	})
	defer srv.Close()

	chunks := []Chunk{
		{Name: "UserSettingsPage", Kind: "function"},
		{Name: "handleSubmit", Kind: "function"},
	}

	result, err := newTestSummarizer(srv.URL).EnrichFile(context.Background(), "settings.tsx", "", chunks)
	if err != nil {
		t.Fatalf("EnrichFile: %v", err)
	}

	// Build context map the same way the caller does.
	ctxMap := make(map[string]string)
	for _, cc := range result.ChunkContexts {
		ctxMap[cc.Name] = cc.Context
	}

	if ctxMap["handleSubmit"] != "Submits the form." {
		t.Errorf("handleSubmit context: got %q", ctxMap["handleSubmit"])
	}
	if ctxMap["UserSettingsPage"] != "Root component for user settings." {
		t.Errorf("UserSettingsPage context: got %q", ctxMap["UserSettingsPage"])
	}
}

func TestEnrichFile_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newTestSummarizer(srv.URL).EnrichFile(context.Background(), "file.go", "content", nil)
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
}

func TestEnrichFile_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(chatResponse{}) // no choices
	}))
	defer srv.Close()

	_, err := newTestSummarizer(srv.URL).EnrichFile(context.Background(), "file.go", "content", nil)
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}

func TestEnrichFile_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outer := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{{Message: struct {
				Content string `json:"content"`
			}{Content: "not valid json"}}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(outer)
	}))
	defer srv.Close()

	_, err := newTestSummarizer(srv.URL).EnrichFile(context.Background(), "file.go", "content", nil)
	if err == nil {
		t.Fatal("expected error for invalid inner JSON")
	}
}

func TestEnrichFile_ContentTruncation(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		capturedBody = b

		inner := enrichJSON{FileSummary: "summary"}
		innerBytes, _ := json.Marshal(inner)
		outer := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{{Message: struct {
				Content string `json:"content"`
			}{Content: string(innerBytes)}}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(outer)
	}))
	defer srv.Close()

	longContent := strings.Repeat("x", maxContentChars+1000)
	_, err := newTestSummarizer(srv.URL).EnrichFile(context.Background(), "file.go", longContent, nil)
	if err != nil {
		t.Fatalf("EnrichFile: %v", err)
	}

	// The captured request body should contain the truncated content.
	body := string(capturedBody)
	if strings.Contains(body, longContent) {
		t.Error("expected content to be truncated in request, but full content was sent")
	}
	// Verify the truncated content (exactly maxContentChars chars of 'x') is present.
	truncated := strings.Repeat("x", maxContentChars)
	if !strings.Contains(body, truncated) {
		t.Error("expected truncated content in request body")
	}
}

func TestEnrichFile_EmptyChunks(t *testing.T) {
	srv := mockEnrichServer(t, "This file is empty.", []ChunkContext{})
	defer srv.Close()

	result, err := newTestSummarizer(srv.URL).EnrichFile(context.Background(), "empty.go", "", nil)
	if err != nil {
		t.Fatalf("EnrichFile: %v", err)
	}
	if result.FileSummary != "This file is empty." {
		t.Errorf("FileSummary: got %q", result.FileSummary)
	}
	if len(result.ChunkContexts) != 0 {
		t.Errorf("expected 0 chunk contexts, got %d", len(result.ChunkContexts))
	}
}

func TestEnrichFile_ConnectionRefused(t *testing.T) {
	_, err := newTestSummarizer("http://127.0.0.1:19998").EnrichFile(context.Background(), "file.go", "content", nil)
	if err == nil {
		t.Fatal("expected error when server is unreachable")
	}
}

func TestExtLang(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"src/app.ts", "typescript"},
		{"components/Button.tsx", "tsx"},
		{"index.js", "javascript"},
		{"widget.jsx", "javascript"},
		{"script.py", "python"},
		{"README.md", ""},
		{"Makefile", ""},
		{"path/to/FILE.GO", "go"},
	}
	for _, tc := range cases {
		got := extLang(tc.path)
		if got != tc.want {
			t.Errorf("extLang(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
