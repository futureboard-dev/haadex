package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/futureboard-dev/haadex/pkg/engine"
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
	Layers  []string `json:"layers"`
	File    string   `json:"file"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Line    int      `json:"line"`
	Score   float64  `json:"score"`
	Snippet string   `json:"snippet"`
}

func runQuery(cmd *cobra.Command, args []string) error {
	q := args[0]

	dbPath := filepath.Join(".haadex", "symbols.db")
	db, err := engine.NewSQLiteStore(dbPath)
	if err != nil {
		return fmt.Errorf("sqlite: %w", err)
	}
	defer db.Close()

	qdrantURL := getQdrantURL()

	cfg, err := loadConfig(".")
	if err != nil {
		return err
	}

	embedder := engine.NewEmbedder(getOpenAIKey())
	store, err := engine.NewQdrantStore(qdrantURL, cfg.Collection, engine.EmbedDim)
	if err != nil {
		return fmt.Errorf("qdrant: %w", err)
	}
	defer store.Close()

	// Collect raw results from each layer
	symbolic, _ := db.SearchSymbol(q, queryLimit)
	trigram, _ := db.SearchTrigram(q, queryLimit)

	var semantic []engine.SearchResult
	if vec, err := embedder.Embed(cmd.Context(), q); err == nil {
		if sem, err := store.Search(vec, queryLimit); err == nil {
			semantic = sem
		}
	}

	// Rank and merge across layers
	ranked := engine.RankResults(symbolic, trigram, semantic, q)

	// Convert to output results
	results := make([]Result, 0, len(ranked))
	for _, r := range ranked {
		results = append(results, Result{
			Layers:  r.Layers,
			File:    r.File,
			Name:    r.Name,
			Kind:    r.Kind,
			Line:    r.Line,
			Score:   r.Score,
			Snippet: truncate(r.Content, 200),
		})
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
		fmt.Printf("[%s] %s %s (%s:%d) score=%.4f\n", strings.Join(r.Layers, "+"), r.Kind, r.Name, r.File, r.Line, r.Score)
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
