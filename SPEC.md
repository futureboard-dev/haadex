# Plan: Parallel Batch Indexer for Large Codebases

## Context

The haadex indexer is fully sequential: one HTTP call to OpenAI per chunk, zero goroutines, no batching. For a ~900k LOC codebase with ~15,000–25,000 chunks, that is 15k–25k individual HTTP round-trips at ~0.5–1s each — **2–7+ hours**. The fix is straightforward: batch embedding calls + a concurrent pipeline.

**Root cause in `pkg/cmd/index.go:257–274`:**
```go
for _, sub := range engine.SplitChunk(chunk) {
    vec, err := embedder.Embed(cmd.Context(), embedText)  // ONE HTTP call per chunk
    db.Upsert(sub, hash)
    store.Upsert(sub, vec)                                // ONE gRPC call per chunk
}
```

---

## Target Architecture

A 5-stage pipeline using Go channels:

```
[file producer] → fileWorkCh → [N parse workers] → subChunkCh
    → [1 batcher] → batchCh → [M embed workers] → resultCh
    → [1 writer] (SQLite serialized, Qdrant batch)
```

**Defaults:** N = `runtime.NumCPU()`, M = 8 workers, batch size = 100 chunks

**Estimated speedup:** ~50–100× (sequential 20k calls → 200 batched calls × 8 parallel workers)

---

## Changes Required

### 1. `pkg/engine/embed.go` — Add `EmbedBatch` with retry

Add a new request type and method. Keep existing `Embed()` unchanged (backward compat).

```go
type openAIEmbedBatchRequest struct {
    Model string   `json:"model"`
    Input []string `json:"input"`
}

func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
```

- Change HTTP client timeout: 30s → 120s (batches take longer)
- Retry logic: up to 3 attempts with exponential backoff (1s, 2s, 4s)
- On HTTP 429: read `Retry-After` header, sleep that duration, then retry
- Validate `len(result.Data) == len(texts)` before returning
- No new dependencies needed — use `time.Sleep` for backoff

### 2. `pkg/engine/qdrant.go` — Add `UpsertBatch`

```go
func (s *QdrantStore) UpsertBatch(ctx context.Context, chunks []Chunk, vecs [][]float32) error
```

Build the full `[]*qdrant.PointStruct` slice in one shot and make a single gRPC call.
Keep existing `Upsert()` calling `UpsertBatch` internally (or leave as-is — it's only used internally now).

### 3. `pkg/cmd/index.go` — Pipeline rewrite + CLI flags

**New flags in `init()`:**
```go
indexCmd.Flags().IntVar(&indexWorkers,   "workers",    8,   "concurrent embed workers")
indexCmd.Flags().IntVar(&indexBatchSize, "batch-size", 100, "chunks per embedding API call")
```

**New package-level vars:** `indexWorkers int`, `indexBatchSize int`

**Pipeline pseudocode:**

```go
// Phase 1: Pre-filter (serial — keeps SQLite access simple)
// Do all hash checks upfront, build toIndex []fileWork
var toIndex []fileWork
for i, entry := range sortedFiles {
    storedHash, found, _ := db.GetFileHash(entry.rel)
    if !indexForce && found && storedHash == entry.hash {
        skipped++; continue
    }
    if found { db.DeleteByFile(rel); store.DeleteByFile(rel) }
    toIndex = append(toIndex, fileWork{...})
}

// Phase 2: Start pipeline
fileWorkCh  := make(chan fileWork, 32)
subChunkCh  := make(chan subChunk, 512)  // subChunk carries chunk + embedText + fileHash + isLast flag
batchCh     := make(chan []subChunk, indexWorkers*2)
resultCh    := make(chan embeddedResult, 64)

// Producer goroutine
go func() { defer close(fileWorkCh); for _, fw := range toIndex { fileWorkCh <- fw } }()

// Parse workers (runtime.NumCPU())
var parseWg sync.WaitGroup
for i := 0; i < runtime.NumCPU(); i++ {
    parseWg.Add(1)
    go func() {
        defer parseWg.Done()
        for fw := range fileWorkCh {
            content, _ := os.ReadFile(fw.absPath)
            chunks, _ := engine.ParseFile(content, extByLang[ext])
            var allSubs []engine.Chunk
            for _, c := range chunks { c.File = fw.rel; allSubs = append(allSubs, engine.SplitChunk(c)...) }
            for i, sub := range allSubs {
                subChunkCh <- subChunk{chunk: sub, embedText: ..., fileHash: fw.hash, rel: fw.rel, isLast: i==len(allSubs)-1}
            }
        }
    }()
}
go func() { parseWg.Wait(); close(subChunkCh) }()

// Batcher goroutine
go func() {
    defer close(batchCh)
    var batch []subChunk
    for sc := range subChunkCh {
        batch = append(batch, sc)
        if len(batch) >= indexBatchSize { batchCh <- batch; batch = nil }
    }
    if len(batch) > 0 { batchCh <- batch }
}()

// Embed workers (indexWorkers, default 8)
var embedWg sync.WaitGroup
for i := 0; i < indexWorkers; i++ {
    embedWg.Add(1)
    go func() {
        defer embedWg.Done()
        for batch := range batchCh {
            texts := /* extract embedText from each sc */
            vecs, err := embedder.EmbedBatch(cmd.Context(), texts)
            if err != nil { /* log, skip batch */ continue }
            resultCh <- embeddedResult{batch: batch, vecs: vecs}
        }
    }()
}
go func() { embedWg.Wait(); close(resultCh) }()

// Writer goroutine (single — SQLite is not goroutine-safe)
var writeWg sync.WaitGroup
writeWg.Add(1)
go func() {
    defer writeWg.Done()
    for result := range resultCh {
        chunks := /* extract chunks */
        store.UpsertBatch(cmd.Context(), chunks, result.vecs)
        for i, sc := range result.batch {
            db.Upsert(sc.chunk, sha256Hash(sc.chunk.Content))
            indexed++
            if sc.isLast { db.UpsertFileHash(sc.rel, sc.fileHash); total++ }
        }
    }
}()
writeWg.Wait()
```

**Progress reporting:** Replace per-chunk `fmt.Printf` with a ticker goroutine reading `atomic.Int64` counters every 500ms. Stops when `resultCh` is drained. All progress writes go to stdout from this single goroutine.

**New types needed in index.go:**
```go
type fileWork       struct { rel, hash, absPath string }
type subChunk       struct { chunk engine.Chunk; embedText, fileHash, rel string; isLast bool }
type embeddedResult struct { batch []subChunk; vecs [][]float32 }
```

---

## Correctness Notes

- **SQLite safety:** All reads (`GetFileHash`) happen in the pre-filter loop (serial, before pipeline starts). All writes happen in the single writer goroutine. No concurrent SQLite access.
- **`isLast` flag:** Tagged by the parse worker as last sub-chunk of a file. Batcher may mix sub-chunks across files in one batch — that's fine; writer calls `UpsertFileHash` per-file when it sees `isLast`.
- **`--force` flag:** Still works — clears both stores before pre-filter; `toIndex` includes all files.
- **Context cancellation:** `EmbedBatch` passes `ctx` to HTTP request → Ctrl+C aborts cleanly; channels drain.

---

## File Summary

| File | Change |
|------|--------|
| `pkg/engine/embed.go` | Add `EmbedBatch(ctx, []string) ([][]float32, error)`, bump timeout to 120s, retry/backoff |
| `pkg/engine/qdrant.go` | Add `UpsertBatch(ctx, []Chunk, [][]float32) error` |
| `pkg/cmd/index.go` | Pipeline rewrite, new types, new CLI flags, atomic progress |

---

## Verification

1. **Unit test `EmbedBatch`:** Mock HTTP server returning two embeddings; assert positional order and retry on 429.
2. **Integration test small repo:** Run `haadex index` on a ~50-file project; verify chunk counts match sequential run.
3. **Benchmark large repo:** Time `haadex index --workers 8 --batch-size 100` on the 900k LOC target; expect <30 min on Tier 2 OpenAI.
4. **Incremental re-run:** Run index twice — second run should skip all unchanged files (skipped == total).
5. **`--force` still works:** Clears and rebuilds from scratch without errors.
