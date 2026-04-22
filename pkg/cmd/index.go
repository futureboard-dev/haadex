package cmd

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ignore "github.com/sabhiram/go-gitignore"
	"github.com/spf13/cobra"

	"github.com/haadex/haadex/pkg/engine"
)

var (
	indexForce     bool
	indexWorkers   int
	indexBatchSize int
)

var indexCmd = &cobra.Command{
	Use:   "index [path]",
	Short: "Index the codebase",
	Long:  `Scans the project, extracts symbols via Tree-sitter, generates embeddings, and upserts to Qdrant and SQLite.`,
	Args:  cobra.MaximumNArgs(1),
	RunE:  runIndex,
}

func init() {
	indexCmd.Flags().BoolVar(&indexForce, "force", false, "drop and rebuild the entire index from scratch")
	indexCmd.Flags().IntVar(&indexWorkers, "workers", 8, "concurrent embed workers")
	indexCmd.Flags().IntVar(&indexBatchSize, "batch-size", 100, "chunks per embedding API call")
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

type fileWork struct {
	rel, hash, absPath string
}

type subChunk struct {
	chunk     engine.Chunk
	embedText string
	fileHash  string
	rel       string
	isLast    bool
}

type embeddedResult struct {
	batch []subChunk
	vecs  [][]float32
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

	// Collect all .gitignore files (root + nested) in a first pass.
	type gitIgnorer struct {
		baseRel string
		gi      *ignore.GitIgnore
	}
	var ignorers []gitIgnorer
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		if d.Name() == ".gitignore" {
			gi, compErr := ignore.CompileIgnoreFile(path)
			if compErr != nil {
				return nil
			}
			dir := filepath.Dir(path)
			relDir, _ := filepath.Rel(root, dir)
			ignorers = append(ignorers, gitIgnorer{baseRel: relDir, gi: gi})
		}
		return nil
	})

	isIgnored := func(rel string) bool {
		for _, ig := range ignorers {
			var checkPath string
			if ig.baseRel == "." {
				checkPath = rel
			} else {
				prefix := ig.baseRel + string(filepath.Separator)
				if !strings.HasPrefix(rel, prefix) {
					continue
				}
				checkPath = rel[len(prefix):]
			}
			if ig.gi.MatchesPath(checkPath) {
				return true
			}
		}
		return false
	}

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
			rel, _ := filepath.Rel(root, path)
			if rel != "." && isIgnored(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if isIgnored(rel) {
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

	// Sort files for stable ordering.
	type fileEntry struct {
		rel  string
		hash string
	}
	sortedFiles := make([]fileEntry, 0, len(currentFiles))
	for rel, hash := range currentFiles {
		sortedFiles = append(sortedFiles, fileEntry{rel, hash})
	}
	sort.Slice(sortedFiles, func(i, j int) bool { return sortedFiles[i].rel < sortedFiles[j].rel })

	// Phase 1: Pre-filter (serial) — do all hash checks upfront, build toIndex list.
	var skipped int
	var toIndex []fileWork
	for _, entry := range sortedFiles {
		rel, fileHash := entry.rel, entry.hash

		if !indexForce {
			storedHash, found, err := db.GetFileHash(rel)
			if err == nil && found && storedHash == fileHash {
				skipped++
				continue
			}
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
		toIndex = append(toIndex, fileWork{rel: rel, hash: fileHash, absPath: absPath})
	}

	// Phase 2: Pipeline.
	fileWorkCh := make(chan fileWork, 32)
	subChunkCh := make(chan subChunk, 512)
	batchCh := make(chan []subChunk, indexWorkers*2)
	resultCh := make(chan embeddedResult, 64)

	// Producer goroutine.
	go func() {
		defer close(fileWorkCh)
		for _, fw := range toIndex {
			fileWorkCh <- fw
		}
	}()

	// Parse workers (runtime.NumCPU()).
	var parseWg sync.WaitGroup
	for i := 0; i < runtime.NumCPU(); i++ {
		parseWg.Add(1)
		go func() {
			defer parseWg.Done()
			for fw := range fileWorkCh {
				content, err := os.ReadFile(fw.absPath)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warn: read %s: %v\n", fw.rel, err)
					continue
				}
				ext := strings.ToLower(filepath.Ext(fw.rel))
				lang := extByLang[ext]
				chunks, err := engine.ParseFile(content, lang)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warn: parse %s: %v\n", fw.rel, err)
					continue
				}
				var allSubs []engine.Chunk
				for _, c := range chunks {
					c.File = fw.rel
					allSubs = append(allSubs, engine.SplitChunk(c)...)
				}
				for i, sub := range allSubs {
					subChunkCh <- subChunk{
						chunk:     sub,
						embedText: fmt.Sprintf("// File: %s\n// Symbol: %s (%s)\n%s", sub.File, sub.Name, sub.Kind, sub.Content),
						fileHash:  fw.hash,
						rel:       fw.rel,
						isLast:    i == len(allSubs)-1,
					}
				}
			}
		}()
	}
	go func() { parseWg.Wait(); close(subChunkCh) }()

	// Batcher goroutine.
	go func() {
		defer close(batchCh)
		var batch []subChunk
		for sc := range subChunkCh {
			batch = append(batch, sc)
			if len(batch) >= indexBatchSize {
				batchCh <- batch
				batch = nil
			}
		}
		if len(batch) > 0 {
			batchCh <- batch
		}
	}()

	// Embed workers.
	var embedWg sync.WaitGroup
	for i := 0; i < indexWorkers; i++ {
		embedWg.Add(1)
		go func() {
			defer embedWg.Done()
			for batch := range batchCh {
				texts := make([]string, len(batch))
				for i, sc := range batch {
					texts[i] = sc.embedText
				}
				vecs, err := embedder.EmbedBatch(cmd.Context(), texts)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\nwarn: embed batch: %v\n", err)
					continue
				}
				resultCh <- embeddedResult{batch: batch, vecs: vecs}
			}
		}()
	}
	go func() { embedWg.Wait(); close(resultCh) }()

	// Atomic progress counters read by the ticker goroutine.
	var atomicIndexed, atomicTotal atomic.Int64

	var progressWg sync.WaitGroup
	progressDone := make(chan struct{})
	progressWg.Add(1)
	go func() {
		defer progressWg.Done()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				fmt.Printf("\r  indexed %d chunks from %d files...", atomicIndexed.Load(), atomicTotal.Load())
			case <-progressDone:
				fmt.Printf("\r%-80s\r", "")
				return
			}
		}
	}()

	// Writer goroutine (single — SQLite is not goroutine-safe).
	var writeWg sync.WaitGroup
	writeWg.Add(1)
	go func() {
		defer writeWg.Done()
		for result := range resultCh {
			chunks := make([]engine.Chunk, len(result.batch))
			for i, sc := range result.batch {
				chunks[i] = sc.chunk
			}
			if err := store.UpsertBatch(cmd.Context(), chunks, result.vecs); err != nil {
				fmt.Fprintf(os.Stderr, "\nwarn: qdrant batch upsert: %v\n", err)
			}
			for _, sc := range result.batch {
				hash := sha256Hash(sc.chunk.Content)
				if err := db.Upsert(sc.chunk, hash); err != nil {
					fmt.Fprintf(os.Stderr, "\nwarn: sqlite upsert: %v\n", err)
				}
				atomicIndexed.Add(1)
				if sc.isLast {
					if err := db.UpsertFileHash(sc.rel, sc.fileHash); err != nil {
						fmt.Fprintf(os.Stderr, "\nwarn: store file hash %s: %v\n", sc.rel, err)
					}
					atomicTotal.Add(1)
				}
			}
		}
	}()
	writeWg.Wait()
	close(progressDone)
	progressWg.Wait()

	// Update indexed_at in config.
	now := time.Now()
	cfg.IndexedAt = &now
	if err := saveConfig(root, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warn: update config: %v\n", err)
	}

	fmt.Printf("\n✓ Indexed %d chunks from %d files (%d unchanged)\n",
		atomicIndexed.Load(), atomicTotal.Load(), skipped)
	return nil
}

func sha256Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}
