package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/haadex/haadex/pkg/engine"
)

var (
	queryLimit int
	queryJSON  bool
)

var queryCmd = &cobra.Command{
	Use:   "query <text>",
	Short: "Hybrid search: symbolic → trigram → semantic",
	Args:  cobra.ExactArgs(1),
	RunE:  runQuery,
}

func init() {
	queryCmd.Flags().IntVarP(&queryLimit, "limit", "n", 10, "max results per layer")
	queryCmd.Flags().BoolVar(&queryJSON, "json", false, "output results as JSON")
}

type Result struct {
	Layer   string  `json:"layer"`
	File    string  `json:"file"`
	Name    string  `json:"name"`
	Kind    string  `json:"kind"`
	Line    int     `json:"line"`
	Score   float32 `json:"score,omitempty"`
	Snippet string  `json:"snippet"`
}

func runQuery(cmd *cobra.Command, args []string) error {
	q := args[0]

	dbPath := filepath.Join(".haadex", "symbols.db")
	db, err := engine.NewSQLiteStore(dbPath)
	if err != nil {
		return fmt.Errorf("sqlite: %w", err)
	}
	defer db.Close()

	ollamaURL := getOllamaURL()
	qdrantURL := getQdrantURL()

	embedder := engine.NewEmbedder(ollamaURL)
	store, err := engine.NewQdrantStore(qdrantURL, "haadex", 512)
	if err != nil {
		return fmt.Errorf("qdrant: %w", err)
	}
	defer store.Close()

	var results []Result
	seen := map[string]bool{}

	key := func(r Result) string {
		return fmt.Sprintf("%s:%s", r.File, r.Name)
	}

	// Layer 1: Symbolic (exact name match in SQLite)
	symbolic, err := db.SearchSymbol(q, queryLimit)
	if err == nil {
		for _, s := range symbolic {
			r := Result{Layer: "symbolic", File: s.File, Name: s.Name, Kind: s.Kind, Line: s.Line, Snippet: truncate(s.Content, 200)}
			if !seen[key(r)] {
				seen[key(r)] = true
				results = append(results, r)
			}
		}
	}

	// Layer 2: Trigram FTS5
	trigram, err := db.SearchTrigram(q, queryLimit)
	if err == nil {
		for _, s := range trigram {
			r := Result{Layer: "trigram", File: s.File, Name: s.Name, Kind: s.Kind, Line: s.Line, Snippet: truncate(s.Content, 200)}
			if !seen[key(r)] {
				seen[key(r)] = true
				results = append(results, r)
			}
		}
	}

	// Layer 3: Semantic (Qdrant vector search)
	vec, err := embedder.Embed("search_query: " + q)
	if err == nil {
		if len(vec) > 512 {
			vec = vec[:512]
		}
		semantic, err := store.Search(vec, queryLimit)
		if err == nil {
			for _, s := range semantic {
				r := Result{Layer: "semantic", File: s.File, Name: s.Name, Kind: s.Kind, Line: s.Line, Score: s.Score, Snippet: truncate(s.Content, 200)}
				if !seen[key(r)] {
					seen[key(r)] = true
					results = append(results, r)
				}
			}
		}
	}

	if queryJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return nil
	}

	for _, r := range results {
		fmt.Printf("[%s] %s %s (%s:%d)\n", r.Layer, r.Kind, r.Name, r.File, r.Line)
		if r.Score > 0 {
			fmt.Printf("  score: %.4f\n", r.Score)
		}
		if r.Snippet != "" {
			fmt.Printf("  %s\n", r.Snippet)
		}
		fmt.Println()
	}
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
