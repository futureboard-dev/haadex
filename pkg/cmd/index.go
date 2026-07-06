package cmd

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ignore "github.com/sabhiram/go-gitignore"
	"github.com/spf13/cobra"

	"github.com/futureboard-dev/haadex/pkg/engine"
)

var (
	indexForce         bool
	indexWorkers       int
	indexBatchSize     int
	indexEnrichWorkers int
	indexNoEnrich      bool
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
	indexCmd.Flags().IntVar(&indexEnrichWorkers, "enrich-workers", 4, "concurrent LLM enrichment workers")
	indexCmd.Flags().BoolVar(&indexNoEnrich, "no-enrich", false, "skip contextual enrichment (faster, lower quality)")
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

type parsedFile struct {
	rel     string
	hash    string
	content []byte
	lang    string
	chunks  []engine.Chunk
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

	openAIKey := getOpenAIKey()
	if openAIKey == "" {
		return fmt.Errorf("OPENAI_API_KEY not set; export it before running `haadex index` (the embedder uses OpenAI's text-embedding-3-large)")
	}
	embedder := engine.NewEmbedder(openAIKey)
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

	var summarizer *engine.Summarizer
	if !indexNoEnrich {
		enrichKey := getEnrichmentKey()
		if enrichKey == "" {
			return fmt.Errorf("OPENROUTER_API_KEY not set; set it to enable contextual enrichment, or pass --no-enrich to skip")
		}
		summarizer = engine.NewSummarizer(enrichKey)
	}

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

	// --- Phase 1: hash-check and parse ---
	var toProcess []parsedFile
	skipped := 0
	for _, entry := range sortedFiles {
		rel, fileHash := entry.rel, entry.hash

		if !indexForce {
			storedHash, found, err := db.GetFileHash(rel)
			if err == nil && found && storedHash == fileHash {
				// Only skip if enrichment also succeeded previously. Otherwise
				// a transient LLM failure would permanently leave the file
				// without a summary until its content changes.
				if summarizer == nil {
					skipped++
					continue
				}
				hasSummary, sumErr := db.HasFileSummary(rel)
				if sumErr == nil && hasSummary {
					skipped++
					continue
				}
				// Missing summary → fall through to re-process this file.
				if err := db.DeleteByFile(rel); err != nil {
					fmt.Fprintf(os.Stderr, "warn: delete for re-enrich %s: %v\n", rel, err)
				}
				if err := store.DeleteByFile(rel); err != nil {
					fmt.Fprintf(os.Stderr, "warn: delete qdrant for re-enrich %s: %v\n", rel, err)
				}
				fmt.Printf("  re-enriching %s (missing summary)\n", rel)
			} else if found {
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

		toProcess = append(toProcess, parsedFile{rel, fileHash, content, lang, chunks})
	}

	fmt.Printf("Parsed %d files (%d unchanged)\n", len(toProcess), skipped)

	// --- Phases 2+3: stream enrich → embed → store ---
	//
	// Enrichment and embedding run concurrently: each file flows into the
	// embed pipeline as soon as its enrichment finishes, so embed workers
	// and Qdrant stay busy instead of waiting for the slowest LLM call.
	enrichedCh := make(chan parsedFile, indexEnrichWorkers*2)

	subChunkCh := make(chan subChunk, 512)
	batchCh := make(chan []subChunk, indexWorkers*2)
	resultCh := make(chan embeddedResult, 64)

	// Producer: emit sub-chunks as enriched files arrive.
	go func() {
		defer close(subChunkCh)
		for pf := range enrichedCh {
			var allSubs []engine.Chunk
			for _, c := range pf.chunks {
				c.File = pf.rel
				allSubs = append(allSubs, engine.SplitChunk(c)...)
			}
			for i, sub := range allSubs {
				var embedText string
				if sub.Context != "" {
					embedText = fmt.Sprintf("[Context: %s]\n// File: %s\n// Symbol: %s (%s)\n%s",
						sub.Context, sub.File, sub.Name, sub.Kind, sub.Content)
				} else {
					embedText = fmt.Sprintf("// File: %s\n// Symbol: %s (%s)\n%s",
						sub.File, sub.Name, sub.Kind, sub.Content)
				}
				subChunkCh <- subChunk{
					chunk:     sub,
					embedText: embedText,
					fileHash:  pf.hash,
					rel:       pf.rel,
					isLast:    i == len(allSubs)-1,
				}
			}
		}
	}()

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
	var embedErrs, upsertErrs atomic.Int64
	var firstEmbedErr atomic.Value
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
					embedErrs.Add(1)
					firstEmbedErr.CompareAndSwap(nil, err.Error())
					fmt.Fprintf(os.Stderr, "\r\033[2K⚠  embed batch: %v\n", err)
					continue
				}
				resultCh <- embeddedResult{batch: batch, vecs: vecs}
			}
		}()
	}
	go func() { embedWg.Wait(); close(resultCh) }()

	// Atomic progress counters read by the ticker goroutine.
	var atomicIndexed, atomicTotal atomic.Int64

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
				upsertErrs.Add(1)
				fmt.Fprintf(os.Stderr, "\r\033[2K⚠  qdrant batch upsert: %v\n", err)
				continue
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

	// Feed enrichedCh — concurrent LLM enrichment, or straight pass-through.
	// Each file enters the embed pipeline as soon as it's ready.
	if summarizer != nil && len(toProcess) > 0 {
		total := len(toProcess)
		fmt.Printf("\n🧠 Enriching %d files with LLM context (%d workers)\n\n", total, indexEnrichWorkers)

		var eDone, eFailed atomic.Int64
		var lastFile atomic.Value
		lastFile.Store("")
		start := time.Now()

		renderDone := make(chan struct{})
		var renderWg sync.WaitGroup
		renderWg.Add(1)
		go func() {
			defer renderWg.Done()
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			isTTY := isTerminal()
			render := func(final bool) {
				drawProgressBar(int(eDone.Load()), total, int(eFailed.Load()),
					time.Since(start), lastFile.Load().(string), isTTY, final)
			}
			for {
				select {
				case <-ticker.C:
					render(false)
				case <-renderDone:
					render(true)
					return
				}
			}
		}()

		sem := make(chan struct{}, indexEnrichWorkers)
		var wg sync.WaitGroup
		for i := range toProcess {
			wg.Add(1)
			sem <- struct{}{}
			go func(pf parsedFile) {
				defer wg.Done()
				defer func() { <-sem }()
				lastFile.Store(pf.rel)
				enrichment, err := summarizer.EnrichFile(cmd.Context(), pf.rel, string(pf.content), pf.chunks)
				if err != nil {
					eFailed.Add(1)
					eDone.Add(1)
					fmt.Fprintf(os.Stderr, "\r\033[2K⚠  enrich %s: %v\n", pf.rel, err)
					// Still index without enrichment; next run will retry
					// because HasFileSummary returns false.
					enrichedCh <- pf
					return
				}
				ctxMap := make(map[string]string, len(enrichment.ChunkContexts))
				for _, cc := range enrichment.ChunkContexts {
					ctxMap[cc.Name] = cc.Context
				}
				for j := range pf.chunks {
					pf.chunks[j].Context = ctxMap[pf.chunks[j].Name]
				}
				summaryChunk := engine.Chunk{
					Name:    filepath.Base(pf.rel),
					Kind:    "file_summary",
					File:    pf.rel,
					Line:    1,
					Content: enrichment.FileSummary,
					Context: enrichment.FileSummary,
				}
				pf.chunks = append([]engine.Chunk{summaryChunk}, pf.chunks...)
				eDone.Add(1)
				enrichedCh <- pf
			}(toProcess[i])
		}
		wg.Wait()
		close(enrichedCh)
		close(renderDone)
		renderWg.Wait()
		fmt.Println()
	} else {
		for _, pf := range toProcess {
			enrichedCh <- pf
		}
		close(enrichedCh)
	}

	// Embed progress ticker — starts after enrichment so it doesn't clash
	// with the enrich bar on the same terminal line.
	progressDone := make(chan struct{})
	var progressWg sync.WaitGroup
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

	writeWg.Wait()
	close(progressDone)
	progressWg.Wait()

	indexed := atomicIndexed.Load()
	eCount := embedErrs.Load()
	uCount := upsertErrs.Load()

	if indexed == 0 {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintf(os.Stderr, "✘ Indexing failed: 0 chunks upserted to Qdrant (collection %q)\n", cfg.Collection)
		if eCount > 0 {
			first, _ := firstEmbedErr.Load().(string)
			fmt.Fprintf(os.Stderr, "  • %d embed batch error(s). First: %s\n", eCount, first)
			fmt.Fprintln(os.Stderr, "  • Check OPENAI_API_KEY is valid and has access to text-embedding-3-large.")
		}
		if uCount > 0 {
			fmt.Fprintf(os.Stderr, "  • %d Qdrant upsert error(s). Check %s is reachable.\n", uCount, qdrantURL)
		}
		if eCount == 0 && uCount == 0 {
			fmt.Fprintln(os.Stderr, "  • No errors reported — did the project have any indexable files?")
		}
		fmt.Fprintln(os.Stderr, "  • Not updating indexed_at; re-run after fixing the issue.")
		return fmt.Errorf("indexing produced 0 points")
	}

	if eCount > 0 || uCount > 0 {
		fmt.Fprintf(os.Stderr, "\n⚠  Partial indexing: %d embed error(s), %d upsert error(s). Re-run `haadex index` to retry missing files.\n", eCount, uCount)
	}

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

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func drawProgressBar(done, total, failed int, elapsed time.Duration, lastFile string, isTTY, final bool) {
	if total <= 0 {
		return
	}
	pct := float64(done) / float64(total)
	if pct > 1 {
		pct = 1
	}

	const width = 30
	filled := int(pct * float64(width))
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)

	var eta string
	if done > 0 && done < total {
		perItem := elapsed / time.Duration(done)
		remaining := time.Duration(total-done) * perItem
		eta = fmtDuration(remaining)
	} else if done >= total {
		eta = "done"
	} else {
		eta = "--"
	}

	rate := 0.0
	if elapsed.Seconds() > 0 {
		rate = float64(done) / elapsed.Seconds()
	}

	last := lastFile
	if len(last) > 40 {
		last = "…" + last[len(last)-39:]
	}

	failedStr := ""
	if failed > 0 {
		failedStr = fmt.Sprintf(" \033[31m✘%d\033[0m", failed)
	}

	line := fmt.Sprintf("\033[36m%s\033[0m %3.0f%% \033[2m│\033[0m %d/%d%s \033[2m│\033[0m %.1f/s \033[2m│\033[0m ETA %s \033[2m│\033[0m \033[2m%s\033[0m",
		bar, pct*100, done, total, failedStr, rate, eta, last)

	if isTTY {
		fmt.Printf("\r\033[2K%s", line)
		if final {
			fmt.Println()
		}
	} else if final {
		fmt.Println(line)
	}
}

func fmtDuration(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	s := int((d % time.Minute) / time.Second)
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
