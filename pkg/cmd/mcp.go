package cmd

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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
				"serverInfo":      map[string]any{"name": "haadex", "version": "1.0.0"},
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

	dbPath := filepath.Join(".haadex", "symbols.db")
	db, err := engine.NewSQLiteStore(dbPath)
	if err != nil {
		return "", fmt.Errorf("sqlite: %w", err)
	}
	defer db.Close()

	embedder := engine.NewEmbedder(getOllamaURL())
	store, err := engine.NewQdrantStore(getQdrantURL(), "haadex", 512)
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

	if vec, err := embedder.Embed("search_query: " + query); err == nil {
		if len(vec) > 512 {
			vec = vec[:512]
		}
		if semantic, err := store.Search(vec, limit); err == nil {
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
		return "No results found.", nil
	}

	out, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func handleIndexDir(args map[string]any) (string, error) {
	root := "."
	if v, ok := args["path"].(string); ok && v != "" {
		root = v
	}

	embedder := engine.NewEmbedder(getOllamaURL())
	store, err := engine.NewQdrantStore(getQdrantURL(), "haadex", 512)
	if err != nil {
		return "", fmt.Errorf("qdrant: %w", err)
	}
	defer store.Close()

	dbPath := filepath.Join(".haadex", "symbols.db")
	db, err := engine.NewSQLiteStore(dbPath)
	if err != nil {
		return "", fmt.Errorf("sqlite: %w", err)
	}
	defer db.Close()

	gi, _ := ignore.CompileIgnoreFile(filepath.Join(root, ".gitignore"))

	var total, indexed int
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".haadex" || name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if gi != nil && gi.MatchesPath(rel) {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		lang, ok := extByLang[ext]
		if !ok {
			return nil
		}
		total++
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		chunks, err := engine.ParseFile(content, lang)
		if err != nil {
			return nil
		}
		for _, chunk := range chunks {
			chunk.File = rel
			for _, sub := range engine.SplitChunk(chunk) {
				h := sha256.Sum256([]byte(sub.Content))
				hash := fmt.Sprintf("%x", h)
				vec, err := embedder.Embed("search_document: " + sub.Content)
				if err != nil {
					continue
				}
				if len(vec) > 512 {
					vec = vec[:512]
				}
				db.Upsert(sub, hash)
				store.Upsert(sub, vec)
				indexed++
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Indexed %d chunks from %d files in %s", indexed, total, root), nil
}
