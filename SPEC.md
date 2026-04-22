# Plan: Contextual Enrichment for Search Quality

## Context

After re-indexing a ~900k LOC codebase, semantic search quality regressed. Tiny generic chunks (`const [searchQuery, setSearchQuery] = useState('')`) produce embeddings that match everything at ~0.45–0.51 cosine similarity, drowning out real results. The root cause: **code is embedded without semantic context**. The current embed input is:

```
// File: admin/supporters/page.tsx
// Symbol: searchQuery (variable)
const [searchQuery, setSearchQuery] = useState('')
```

The embedding model has almost nothing to work with. File path metadata dominates the vector. The fix: never embed code in isolation — prepend LLM-generated contextual descriptions that explain *what the code does and why*.

---

## Approach: Per-File LLM Enrichment

Use a fast/cheap LLM (GPT-4o-mini) to generate contextual descriptions at index time. **One LLM call per file** (not per chunk) — the LLM sees the full file and returns descriptions for all extracted symbols at once.

### Before (current)
```
// File: settings/user-v2/page.tsx
// Symbol: handleSubmit (function)
async function handleSubmit(data: FormData) { ... }
```

### After (enriched)
```
[Context: In the user settings page, this function handles form submission for updating user profile details including nickname, email, and notification preferences. It validates input, calls the update API, and shows success/error toast notifications.]
// File: settings/user-v2/page.tsx  
// Symbol: handleSubmit (function)
async function handleSubmit(data: FormData) { ... }
```

The embedding now captures purpose, not just syntax. "user settings page component" → `handleSubmit` is now a strong semantic match.

### File-Level Summary Chunks

Additionally, generate a 2-3 sentence summary per file and store it as a special chunk (`kind: "file_summary"`). This directly addresses broad queries like "user settings page component" — the file summary is a direct semantic match without relying on individual symbol names.

---

## LLM Choice: DeepSeek V3 via OpenRouter

Use OpenRouter (`https://openrouter.ai/api/v1`) with `deepseek/deepseek-chat-v3-0324`. OpenRouter's API is OpenAI-compatible (`/v1/chat/completions`), so the Summarizer uses the same HTTP pattern as the Embedder.

**Config:**
- `OPENROUTER_API_KEY` env var — OpenRouter API key (required for enrichment)
- Base URL and model are constants in `summarizer.go`, not env vars — same pattern as `embed.go`

**Cost analysis** (DeepSeek V3: ~$0.14/1M input, ~$0.28/1M output):

- Per file: ~2000 tokens input, ~150 tokens output
- For 2000 files: ~4M input ($0.56) + ~300K output ($0.08) = **~$0.64 total**
- Negligible cost even for large codebases

---

## Changes Required

### 1. `pkg/engine/summarizer.go` (NEW FILE)

New `Summarizer` struct wrapping OpenRouter chat completions (OpenAI-compatible). Follow the same style as `pkg/engine/embed.go`: constants for URL and model, only the API key comes from the caller.

```go
const (
    enrichBaseURL = "https://openrouter.ai/api/v1"
    enrichModel   = "deepseek/deepseek-chat-v3-0324"
)

type Summarizer struct {
    apiKey string
    client *http.Client
}

type ChunkContext struct {
    Name    string `json:"name"`
    Context string `json:"context"`
}

type FileEnrichment struct {
    FileSummary   string         // 2-3 sentence file summary
    ChunkContexts []ChunkContext // per-symbol context descriptions
}

func NewSummarizer(apiKey string) *Summarizer

// EnrichFile sends one LLM call per file. Returns descriptions for all chunks + file summary.
func (s *Summarizer) EnrichFile(ctx context.Context, filePath string, content string, chunks []Chunk) (*FileEnrichment, error)
```

**Prompt design for `EnrichFile`:**

```
You are a code indexer. Given a source file and its symbols, produce:
1. A 2-3 sentence summary of the file's purpose.
2. For each symbol, a 1-2 sentence description of what it does and its role in the file.

File: {filePath}
Symbols: {name (kind), name (kind), ...}

```{lang}
{content, truncated to ~6000 chars}
```

Respond with JSON:
{
  "file_summary": "...",
  "symbols": [{"name": "...", "context": "..."}, ...]
}
```

**Key decisions:**
- Truncate file content to ~6000 chars (~1500 tokens) to keep costs low and stay within context
- Use structured JSON output for reliable parsing
- Include symbol names in the prompt so the LLM knows exactly which symbols to describe
- Retry with backoff on failure (same pattern as `EmbedBatch`)
- If the LLM call fails, fall back gracefully — index without context (degraded but not broken)

### 2. `pkg/engine/sqlite.go` — Schema migration

Add `context` column to `symbols` table:

```sql
ALTER TABLE symbols ADD COLUMN context TEXT NOT NULL DEFAULT '';
```

**Migration approach:** In `createSchema()`, after creating tables, run `ALTER TABLE` wrapped in a check:
```go
// Add context column if it doesn't exist (migration for existing indices)
s.db.Exec(`ALTER TABLE symbols ADD COLUMN context TEXT NOT NULL DEFAULT ''`)
// sqlite returns error if column exists — safe to ignore
```

Update `Upsert()` to store context. Update `symbols_fts` triggers to include context in the FTS index (so trigram search also benefits from context text).

Updated FTS schema:
```sql
CREATE VIRTUAL TABLE IF NOT EXISTS symbols_fts USING fts5(
    name, kind, file, content, context,
    tokenize='trigram'
);
```

**Note:** FTS schema change requires dropping and recreating the virtual table on migration. Detect this by checking if the `context` column exists in `symbols_fts`.

### 3. `pkg/engine/qdrant.go` — Add context to payload

In `Upsert()` / `UpsertBatch()`, add context to payload:

```go
"context": {Kind: &qdrant.Value_StringValue{StringValue: chunk.Context}},
```

No schema change needed — Qdrant is schemaless.

### 4. `pkg/engine/parser.go` — Add Context field to Chunk

```go
type Chunk struct {
    Name       string
    Kind       string
    File       string
    Line       int
    Content    string
    Context    string // LLM-generated contextual description
    ParentName string
}
```

### 5. `pkg/cmd/index.go` — Integrate enrichment into pipeline

The enrichment step runs **per file, after parsing, before embedding**. In the pipeline:

```
[parse workers] → parsedFileCh → [enrichment workers] → enrichedChunkCh → [embed batcher] → ...
```

**New channel type:**

```go
type parsedFile struct {
    rel      string
    hash     string
    content  []byte
    lang     string
    chunks   []engine.Chunk // all chunks from this file (pre-split)
}
```

**Enrichment workers (M = 4 concurrent, configurable via --enrich-workers):**

```go
for pf := range parsedFileCh {
    // Call summarizer — one LLM call per file
    enrichment, err := summarizer.EnrichFile(ctx, pf.rel, string(pf.content), pf.chunks)
    if err != nil {
        // Fallback: proceed without context (log warning)
    } else {
        // Attach context to each chunk by matching on name
        contextMap := map[string]string{}
        for _, cc := range enrichment.ChunkContexts {
            contextMap[cc.Name] = cc.Context
        }
        for i := range pf.chunks {
            pf.chunks[i].Context = contextMap[pf.chunks[i].Name]
        }
        
        // Create file summary chunk
        summaryChunk := engine.Chunk{
            Name:    filepath.Base(pf.rel),
            Kind:    "file_summary",
            File:    pf.rel,
            Line:    1,
            Content: enrichment.FileSummary,
            Context: enrichment.FileSummary,
        }
        // send summaryChunk downstream
    }
    
    // SplitChunk + send all sub-chunks downstream with enriched embedText
    for i, chunk := range allSubs {
        embedText := fmt.Sprintf("[Context: %s]\n// File: %s\n// Symbol: %s (%s)\n%s",
            chunk.Context, chunk.File, chunk.Name, chunk.Kind, chunk.Content)
        // send to subChunkCh
    }
}
```

**Updated pipeline (6 stages):**

```
                    fileWorkCh          parsedFileCh          enrichedChunkCh
[file producer] ──────────────► [N parse  ──────────────► [M enrichment  ──────────────►
                                 workers]                  workers (LLM)]

     batchCh              resultCh
──────────────► [K embed  ──────────────► [1 writer]
  [1 batcher]    workers]                 (SQLite + Qdrant)
```

**New CLI flags:**
```go
indexCmd.Flags().IntVar(&indexEnrichWorkers, "enrich-workers", 4, "concurrent LLM enrichment workers")
indexCmd.Flags().BoolVar(&indexNoEnrich, "no-enrich", false, "skip contextual enrichment (faster, lower quality)")
```

**Env var helper** (add to `pkg/cmd/config.go`):
```go
func getEnrichmentKey() string { return os.Getenv("OPENROUTER_API_KEY") }
```

**Summarizer initialization in `runIndex`:**
```go
var summarizer *engine.Summarizer
if !indexNoEnrich {
    enrichKey := getEnrichmentKey()
    if enrichKey == "" {
        fmt.Fprintln(os.Stderr, "warn: OPENROUTER_API_KEY not set, skipping contextual enrichment")
    } else {
        summarizer = engine.NewSummarizer(enrichKey)
    }
}
```

### 6. `pkg/engine/ranker.go` — Boost file_summary results for broad queries

Add kind boost for `file_summary`:

```go
func kindBoost(kind string) float64 {
    switch kind {
    case "file_summary":
        return 0.08
    case "function", "method", "class", "interface", "struct", "type":
        return 0.05
    default:
        return 0
    }
}
```

### 7. Keep the `minVariableChars` filter in `parser.go`

Belt-and-suspenders: even with enrichment, tiny variable declarations are rarely useful search targets. Keeping the filter also saves LLM tokens by not enriching throwaway chunks. No changes needed — it's already in place.

---

## Embed Text Format Comparison

### Current (broken for small chunks)
```
// File: admin/supporters/page.tsx
// Symbol: searchQuery (variable)
const [searchQuery, setSearchQuery] = useState('')
```

### Enriched (context carries the semantic signal)
```
[Context: This state variable in the admin supporters page manages the search filter for querying supporter records in the admin table view.]
// File: admin/supporters/page.tsx
// Symbol: searchQuery (variable)
const [searchQuery, setSearchQuery] = useState('')
```

### File summary (catches broad queries)
```
[Context: The admin supporters page displays a searchable, paginated table of supporter records with filtering, bulk actions, and CRUD operations for managing supporter accounts.]
// File: admin/supporters/page.tsx
// Symbol: page.tsx (file_summary)
The admin supporters page displays a searchable, paginated table of supporter records...
```

---

## Correctness Notes

- **Graceful degradation:** If `EnrichFile` fails (API error, timeout), the chunk is indexed without context — same as current behavior. No data loss.
- **`--no-enrich` flag:** Allows fast indexing without LLM calls when quality isn't needed (e.g., quick re-index during development).
- **Schema migration:** The `ALTER TABLE` approach is idempotent. Existing indices work without re-indexing — context is just empty until `--force` re-index.
- **File hash skip still works:** Unchanged files skip both enrichment AND embedding. Context is regenerated only for changed files.
- **Incremental consistency:** File summary chunks use the same file hash tracking. When a file changes, old summary + symbol chunks are deleted and regenerated.

---

## File Summary

| File | Change |
|------|--------|
| `pkg/engine/summarizer.go` | **NEW** — `Summarizer` struct, `EnrichFile` method, LLM prompt, JSON parsing |
| `pkg/engine/parser.go` | Add `Context` field to `Chunk` (keep `minVariableChars` filter) |
| `pkg/engine/sqlite.go` | Add `context` column, update FTS triggers, update `Upsert` |
| `pkg/engine/qdrant.go` | Add `context` to Qdrant payload in `Upsert`/`UpsertBatch` |
| `pkg/cmd/index.go` | Add enrichment stage to pipeline, `--enrich-workers`, `--no-enrich` flags |
| `pkg/engine/ranker.go` | Boost `file_summary` kind in `kindBoost()` |

---

## Verification

1. **Unit test `EnrichFile`:** Mock HTTP server returning JSON with file summary + chunk contexts; verify parsing and name matching.
2. **Index small repo:** Run `haadex index --force` on a ~50-file project; verify each chunk has non-empty `context` in SQLite.
3. **Regression tests against the 4 failing queries:**
   - "user settings page component" → should match `UserSettingsHubPage` or file summary for `settings/user/page.tsx`
   - "route configuration paths" → should match `DiscoveredRoute` / `AvailableRoutes`
   - "authentication middleware" → should match `checkAuth` function
   - "e2e test user settings form submit" → should match `user-settings.spec.ts`
4. **`--no-enrich` flag:** Verify indexing works without LLM calls (same as current behavior).
5. **Incremental re-run:** Change one file, re-index — only that file should be re-enriched and re-embedded.
6. **Cost check:** Monitor OpenAI billing after indexing 900k LOC — should be < $2.
