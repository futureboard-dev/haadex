package cmd

import (
	"crypto/sha256"
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

var indexForce bool

var indexCmd = &cobra.Command{
	Use:   "index [path]",
	Short: "Index the codebase",
	Long:  `Scans the project, extracts symbols via Tree-sitter, generates embeddings, and upserts to Qdrant and SQLite.`,
	Args:  cobra.MaximumNArgs(1),
	RunE:  runIndex,
}

func init() {
	indexCmd.Flags().BoolVar(&indexForce, "force", false, "drop and rebuild the entire index from scratch")
}

// extByLang maps file extensions to tree-sitter language IDs.
// Add new languages here alongside a corresponding entry in engine.registry.
var extByLang = map[string]string{
	".go":  "go",
	".js":  "javascript",
	".jsx": "javascript",
	".ts":  "typescript",
	".tsx": "tsx",
	".py":  "python",
}

func runIndex(cmd *cobra.Command, args []string) error {
	root := "."
	if len(args) > 0 {
		root = args[0]
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}

	cfg, err := loadConfig(root)
	if err != nil {
		// No config yet — auto-init config without docker-compose
		cfg = &HaadexConfig{
			Root:       absRoot,
			Collection: deriveCollection(absRoot),
		}
		if mkErr := os.MkdirAll(filepath.Join(root, haadexDir), 0755); mkErr != nil {
			return fmt.Errorf("create .haadex dir: %w", mkErr)
		}
		if mkErr := saveConfig(root, cfg); mkErr != nil {
			return fmt.Errorf("write config: %w", mkErr)
		}
	}

	qdrantURL := getQdrantURL()

	embedder := engine.NewEmbedder(getOpenAIKey())
	store, err := engine.NewQdrantStore(qdrantURL, cfg.Collection, engine.EmbedDim)
	if err != nil {
		return fmt.Errorf("qdrant: %w", err)
	}
	defer store.Close()

	dbPath := filepath.Join(root, haadexDir, "symbols.db")
	db, err := engine.NewSQLiteStore(dbPath)
	if err != nil {
		return fmt.Errorf("sqlite: %w", err)
	}
	defer db.Close()

	if indexForce {
		fmt.Println("Force mode: clearing existing index...")
		if err := db.Clear(); err != nil {
			return fmt.Errorf("clear sqlite: %w", err)
		}
		if err := store.ResetCollection(); err != nil {
			return fmt.Errorf("reset qdrant: %w", err)
		}
	}

	gi, _ := ignore.CompileIgnoreFile(filepath.Join(root, ".gitignore"))

	// Collect all current source files on disk.
	currentFiles := map[string]string{} // rel path -> file hash
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
	if err != nil {
		return err
	}

	// Remove stale files (indexed but no longer on disk).
	if !indexForce {
		indexedFiles, err := db.ListFiles()
		if err != nil {
			return fmt.Errorf("list indexed files: %w", err)
		}
		for _, f := range indexedFiles {
			if _, exists := currentFiles[f]; !exists {
				fmt.Printf("  removed %s\n", f)
				if err := db.DeleteByFile(f); err != nil {
					fmt.Fprintf(os.Stderr, "warn: delete sqlite %s: %v\n", f, err)
				}
				if err := store.DeleteByFile(f); err != nil {
					fmt.Fprintf(os.Stderr, "warn: delete qdrant %s: %v\n", f, err)
				}
			}
		}
	}

	// Index new and changed files.
	var total, indexed, skipped int
	for rel, fileHash := range currentFiles {
		if !indexForce {
			storedHash, found, err := db.GetFileHash(rel)
			if err == nil && found && storedHash == fileHash {
				skipped++
				continue
			}
			// File changed — clear old symbols before reindexing.
			if found {
				if err := db.DeleteByFile(rel); err != nil {
					fmt.Fprintf(os.Stderr, "warn: delete stale %s: %v\n", rel, err)
				}
				if err := store.DeleteByFile(rel); err != nil {
					fmt.Fprintf(os.Stderr, "warn: delete stale qdrant %s: %v\n", rel, err)
				}
			}
		}

		absPath := filepath.Join(root, rel)
		content, err := os.ReadFile(absPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: read %s: %v\n", rel, err)
			continue
		}

		ext := strings.ToLower(filepath.Ext(rel))
		lang := extByLang[ext]

		chunks, err := engine.ParseFile(content, lang)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: parse %s: %v\n", rel, err)
			continue
		}

		total++
		fmt.Printf("  embedding %s...\n", rel)
		for _, chunk := range chunks {
			chunk.File = rel
			for _, sub := range engine.SplitChunk(chunk) {
				hash := sha256Hash(sub.Content)

				fmt.Printf("    chunk %s\r", sub.Name)
				// Enrich embedding input with file path and symbol metadata
				embedText := fmt.Sprintf("// File: %s\n// Symbol: %s (%s)\n%s", sub.File, sub.Name, sub.Kind, sub.Content)
				vec, err := embedder.Embed(cmd.Context(), embedText)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\nwarn: embed %s:%s: %v\n", rel, sub.Name, err)
					continue
				}

				if err := db.Upsert(sub, hash); err != nil {
					fmt.Fprintf(os.Stderr, "warn: sqlite upsert: %v\n", err)
				}
				if err := store.Upsert(sub, vec); err != nil {
					fmt.Fprintf(os.Stderr, "warn: qdrant upsert: %v\n", err)
				}
				indexed++
			}
		}

		if err := db.UpsertFileHash(rel, fileHash); err != nil {
			fmt.Fprintf(os.Stderr, "warn: store file hash %s: %v\n", rel, err)
		}

		fmt.Printf("  indexed %s (%d chunks)\n", rel, len(chunks))
	}

	// Update indexed_at in config.
	now := time.Now()
	cfg.IndexedAt = &now
	if err := saveConfig(root, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warn: update config: %v\n", err)
	}

	fmt.Printf("\n✓ Indexed %d chunks from %d files (%d unchanged)\n", indexed, total, skipped)
	return nil
}

func sha256Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}
