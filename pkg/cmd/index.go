package cmd

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
	"github.com/spf13/cobra"

	"github.com/haadex/haadex/pkg/engine"
)

var indexCmd = &cobra.Command{
	Use:   "index [path]",
	Short: "Index the codebase",
	Long:  `Scans the project, extracts symbols via Tree-sitter, generates embeddings, and upserts to Qdrant and SQLite.`,
	Args:  cobra.MaximumNArgs(1),
	RunE:  runIndex,
}

// extByLang maps file extensions to tree-sitter language IDs.
// Add new languages here alongside a corresponding entry in engine.registry.
var extByLang = map[string]string{
	".go":  "go",
	".ts":  "typescript",
	".tsx": "tsx",
	".py":  "python",
}

func runIndex(cmd *cobra.Command, args []string) error {
	root := "."
	if len(args) > 0 {
		root = args[0]
	}

	ollamaURL := getOllamaURL()
	qdrantURL := getQdrantURL()

	embedder := engine.NewEmbedder(ollamaURL)
	store, err := engine.NewQdrantStore(qdrantURL, "haadex", 512)
	if err != nil {
		return fmt.Errorf("qdrant: %w", err)
	}
	defer store.Close()

	dbPath := filepath.Join(".haadex", "symbols.db")
	db, err := engine.NewSQLiteStore(dbPath)
	if err != nil {
		return fmt.Errorf("sqlite: %w", err)
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
			fmt.Fprintf(os.Stderr, "warn: read %s: %v\n", path, err)
			return nil
		}

		chunks, err := engine.ParseFile(content, lang)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: parse %s: %v\n", path, err)
			return nil
		}

		for _, chunk := range chunks {
			chunk.File = rel
			hash := sha256Hash(chunk.Content)

			vec, err := embedder.Embed("search_document: " + chunk.Content)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: embed %s:%s: %v\n", rel, chunk.Name, err)
				continue
			}
			// Truncate to 512 dimensions (Matryoshka)
			if len(vec) > 512 {
				vec = vec[:512]
			}

			if err := db.Upsert(chunk, hash); err != nil {
				fmt.Fprintf(os.Stderr, "warn: sqlite upsert: %v\n", err)
			}
			if err := store.Upsert(chunk, vec); err != nil {
				fmt.Fprintf(os.Stderr, "warn: qdrant upsert: %v\n", err)
			}
			indexed++
		}

		fmt.Printf("  indexed %s (%d chunks)\n", rel, len(chunks))
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Printf("\n✓ Indexed %d chunks from %d files\n", indexed, total)
	return nil
}

func sha256Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}
