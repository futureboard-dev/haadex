package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	ignore "github.com/sabhiram/go-gitignore"
	"github.com/spf13/cobra"

	"github.com/haadex/haadex/pkg/engine"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start MCP server (JSON-RPC over stdio)",
	Long:  `Starts a Model Context Protocol server over stdin/stdout for use with Claude Code.`,
	RunE:  runMCP,
}

// ---------------------------------------------------------------------------
// JSON-RPC types
// ---------------------------------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// MCP schema types
// ---------------------------------------------------------------------------

type toolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]schemaProp `json:"properties"`
	Required   []string            `json:"required"`
}

type schemaProp struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

var mcpTools = []toolDef{
	{
		Name:        "search_code",
		Description: "Search the indexed codebase using symbolic, trigram, and semantic layers. Returns matching functions, methods, structs, and classes with file paths and line numbers.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"query": {Type: "string", Description: "Natural language or identifier to search for"},
				"limit": {Type: "number", Description: "Max results per layer (default 5)"},
			},
			Required: []string{"query"},
		},
	},
	{
		Name:        "index_dir",
		Description: "Index a directory into haadex so it can be searched. Run this when starting work on a new project or after major code changes.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"path": {Type: "string", Description: "Directory path to index (default: current directory)"},
			},
			Required: []string{},
		},
	},
}

// ---------------------------------------------------------------------------
// MCP server loop
// ---------------------------------------------------------------------------

func runMCP(cmd *cobra.Command, args []string) error {
	enc := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			enc.Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}

		var resp rpcResponse
		resp.JSONRPC = "2.0"
		resp.ID = req.ID

		switch req.Method {
		case "initialize":
			resp.Result = map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "haadex", "version": Version},
			}

		case "notifications/initialized":
			continue // no response needed

		case "tools/list":
			resp.Result = map[string]any{"tools": mcpTools}

		case "tools/call":
			var p toolCallParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				resp.Error = &rpcError{Code: -32602, Message: "invalid params"}
				break
			}
			text, err := handleToolCall(p)
			if err != nil {
				resp.Result = map[string]any{
					"content": []contentBlock{{Type: "text", Text: "error: " + err.Error()}},
					"isError": true,
				}
			} else {
				resp.Result = map[string]any{
					"content": []contentBlock{{Type: "text", Text: text}},
				}
			}

		default:
			resp.Error = &rpcError{Code: -32601, Message: "method not found"}
		}

		enc.Encode(resp)
	}

	return scanner.Err()
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func handleToolCall(p toolCallParams) (string, error) {
	switch p.Name {
	case "search_code":
		return handleSearchCode(p.Arguments)
	case "index_dir":
		return handleIndexDir(p.Arguments)
	default:
		return "", fmt.Errorf("unknown tool: %s", p.Name)
	}
}

func handleSearchCode(args map[string]any) (string, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return "", fmt.Errorf("query is required")
	}

	limit := 5
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}

	cfg, err := loadConfig(".")
	if err != nil {
		return "", err
	}

	dbPath := filepath.Join(haadexDir, "symbols.db")
	db, err := engine.NewSQLiteStore(dbPath)
	if err != nil {
		return "", fmt.Errorf("sqlite: %w", err)
	}
	defer db.Close()

	embedder := engine.NewEmbedder(getOpenAIKey())
	store, err := engine.NewQdrantStore(getQdrantURL(), cfg.Collection, engine.EmbedDim)
	if err != nil {
		return "", fmt.Errorf("qdrant: %w", err)
	}
	defer store.Close()

	var results []Result
	seen := map[string]bool{}
	key := func(r Result) string { return fmt.Sprintf("%s:%s", r.File, r.Name) }

	if symbolic, err := db.SearchSymbol(query, limit); err == nil {
		for _, s := range symbolic {
			r := Result{Layer: "symbolic", File: s.File, Name: s.Name, Kind: s.Kind, Line: s.Line, Snippet: truncate(s.Content, 300)}
			if !seen[key(r)] {
				seen[key(r)] = true
				results = append(results, r)
			}
		}
	}

	if trigram, err := db.SearchTrigram(query, limit); err == nil {
		for _, s := range trigram {
			r := Result{Layer: "trigram", File: s.File, Name: s.Name, Kind: s.Kind, Line: s.Line, Snippet: truncate(s.Content, 300)}
			if !seen[key(r)] {
				seen[key(r)] = true
				results = append(results, r)
			}
		}
	}

	var warnings []string

	vec, embedErr := embedder.Embed(context.Background(), "search_query: "+query)
	if embedErr != nil {
		warnings = append(warnings, fmt.Sprintf("semantic layer unavailable: %v", embedErr))
	} else {
		semantic, searchErr := store.Search(vec, limit)
		if searchErr != nil {
			warnings = append(warnings, fmt.Sprintf("semantic search failed: %v", searchErr))
		} else {
			for _, s := range semantic {
				r := Result{Layer: "semantic", File: s.File, Name: s.Name, Kind: s.Kind, Line: s.Line, Score: s.Score, Snippet: truncate(s.Content, 300)}
				if !seen[key(r)] {
					seen[key(r)] = true
					results = append(results, r)
				}
			}
		}
	}

	if len(results) == 0 {
		if len(warnings) > 0 {
			return "No results found.\n\nWarnings:\n- " + strings.Join(warnings, "\n- "), nil
		}
		return "No results found.", nil
	}

	out, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", err
	}
	if len(warnings) > 0 {
		return string(out) + "\n\nWarnings:\n- " + strings.Join(warnings, "\n- "), nil
	}
	return string(out), nil
}

func handleIndexDir(args map[string]any) (string, error) {
	root := "."
	if v, ok := args["path"].(string); ok && v != "" {
		root = v
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}

	cfg, err := loadConfig(root)
	if err != nil {
		cfg = &HaadexConfig{
			Root:       absRoot,
			Collection: deriveCollection(absRoot),
		}
		if mkErr := os.MkdirAll(filepath.Join(root, haadexDir), 0755); mkErr != nil {
			return "", fmt.Errorf("create .haadex dir: %w", mkErr)
		}
		if mkErr := saveConfig(root, cfg); mkErr != nil {
			return "", fmt.Errorf("write config: %w", mkErr)
		}
	}

	embedder := engine.NewEmbedder(getOpenAIKey())
	store, err := engine.NewQdrantStore(getQdrantURL(), cfg.Collection, engine.EmbedDim)
	if err != nil {
		return "", fmt.Errorf("qdrant: %w", err)
	}
	defer store.Close()

	dbPath := filepath.Join(root, haadexDir, "symbols.db")
	db, err := engine.NewSQLiteStore(dbPath)
	if err != nil {
		return "", fmt.Errorf("sqlite: %w", err)
	}
	defer db.Close()

	gi, _ := ignore.CompileIgnoreFile(filepath.Join(root, ".gitignore"))

	// Collect current files and their hashes.
	currentFiles := map[string]string{}
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == haadexDir || name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if gi != nil && gi.MatchesPath(rel) {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if _, ok := extByLang[ext]; !ok {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		h := sha256.Sum256(content)
		currentFiles[rel] = fmt.Sprintf("%x", h)
		return nil
	})

	// Remove stale files.
	indexedFiles, _ := db.ListFiles()
	for _, f := range indexedFiles {
		if _, exists := currentFiles[f]; !exists {
			db.DeleteByFile(f)
			store.DeleteByFile(f)
		}
	}

	// Index new and changed files.
	var total, indexed, skipped int
	for rel, fileHash := range currentFiles {
		storedHash, found, err := db.GetFileHash(rel)
		if err == nil && found && storedHash == fileHash {
			skipped++
			continue
		}
		if found {
			db.DeleteByFile(rel)
			store.DeleteByFile(rel)
		}

		absPath := filepath.Join(root, rel)
		content, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		ext := strings.ToLower(filepath.Ext(rel))
		lang := extByLang[ext]
		chunks, err := engine.ParseFile(content, lang)
		if err != nil {
			continue
		}
		total++
		for _, chunk := range chunks {
			chunk.File = rel
			for _, sub := range engine.SplitChunk(chunk) {
				h := sha256.Sum256([]byte(sub.Content))
				hash := fmt.Sprintf("%x", h)
				vec, err := embedder.Embed(context.Background(), "search_document: " + sub.Content)
				if err != nil {
					continue
				}
				db.Upsert(sub, hash)
				store.Upsert(sub, vec)
				indexed++
			}
		}
		db.UpsertFileHash(rel, fileHash)
	}

	now := time.Now()
	cfg.IndexedAt = &now
	saveConfig(root, cfg)

	return fmt.Sprintf("Indexed %d chunks from %d files (%d unchanged) in %s", indexed, total, skipped, root), nil
}
