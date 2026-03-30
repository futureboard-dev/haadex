# Haadex

A portable, local-first code indexer for AI agents. Haadex manages its own infrastructure (Qdrant + Ollama) via Docker Compose and implements a **triple-layer retrieval system** so AI tools get precise, contextual answers about any codebase.

## How it works

```
haadex index  →  Tree-sitter AST  →  SQLite (symbols + trigram FTS5)
                                  →  Qdrant  (512-dim Nomic vectors)

haadex query  →  Layer 1: Symbolic  (exact/partial name match in SQLite)
              →  Layer 2: Trigram   (FTS5 substring search in SQLite)
              →  Layer 3: Semantic  (vector nearest-neighbour in Qdrant)
              →  JSON results
```

All data (vector store, model weights, SQLite DB) lives inside `.haadex/` — one directory per project, fully isolated.

---

## Prerequisites

- **Docker** (with Compose v2 or docker-compose v1)
- **Go 1.21+** (to build from source)

---

## Installation

```bash
git clone https://github.com/haadex/haadex
cd haadex
go build -o haadex .
# optionally move to PATH
mv haadex /usr/local/bin/haadex
```

---

## Quick start

```bash
# 1. Go to your project
cd /path/to/your/project

# 2. Initialize Haadex (creates .haadex/ and docker-compose.yml)
haadex init

# 3. Start containers and pull the embedding model (first run downloads ~300 MB)
haadex up

# 4. Index the codebase
haadex index

# 5. Query
haadex query "authentication middleware"
haadex query "database connection pool" --json
```

---

## Commands

### `haadex init`

Creates `.haadex/` in the current directory with:

```
.haadex/
├── docker-compose.yml   ← Qdrant + Ollama services
├── qdrant_storage/      ← vector DB data (persisted)
└── ollama_storage/      ← model weights (persisted)
```

### `haadex up`

Starts Qdrant and Ollama via Docker Compose, then pulls the `nomic-embed-text` model into Ollama if it isn't already present.

```bash
haadex up
```

Services exposed on localhost:
| Service | Port  |
|---------|-------|
| Qdrant REST | 6333 |
| Qdrant gRPC | 6334 |
| Ollama      | 11434 |

### `haadex index [path]`

Walks the project directory (respecting `.gitignore`), extracts symbols via Tree-sitter, generates Nomic embeddings via Ollama, and upserts into both SQLite and Qdrant.

```bash
haadex index           # index current directory
haadex index ./src     # index a subdirectory
```

**Supported languages:**

| Language   | Extensions     |
|------------|----------------|
| Go         | `.go`          |
| TypeScript | `.ts`          |
| TSX        | `.tsx`         |
| Python     | `.py`          |

**What gets extracted:**

- Go: `function`, `method`, `struct`, `interface`, `type`
- TypeScript/TSX: `function`, `arrow function`, `class`, `interface`, `type`
- Python: `function`, `class`

**Embedding details:**
- Model: `nomic-embed-text` (via Ollama)
- Prefix: `search_document:` at index time, `search_query:` at query time
- Dimensions: truncated to **512** (Matryoshka) for memory efficiency
- **Large symbol splitting:** symbols whose source exceeds 4 000 characters are automatically split into overlapping sub-chunks (5-line overlap). Sub-chunks are named `SymbolName[1/N]` and carry a `parent_name` field linking them back to the original symbol, so search results always trace to the right declaration.

### `haadex query "<text>"`

Runs a hybrid three-layer search and prints results.

```bash
haadex query "parse JWT token"
haadex query "database migration" --json   # machine-readable output
haadex query "HTTP handler" -n 5           # limit to 5 results per layer
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | false | Output results as JSON array |
| `-n`, `--limit` | 10 | Max results per layer |

**Output fields:**

```json
[
  {
    "layer":   "semantic",
    "file":    "pkg/server/handler.go",
    "name":    "AuthMiddleware",
    "kind":    "function",
    "line":    42,
    "score":   0.9231,
    "snippet": "func AuthMiddleware(next http.Handler) http.Handler {..."
  },
  {
    "layer":   "semantic",
    "file":    "src/app/dashboard/CreateConsultationForm.tsx",
    "name":    "CreateConsultationForm[2/3]",
    "kind":    "function",
    "line":    1,
    "score":   0.8912,
    "snippet": "...",
    "parent":  "CreateConsultationForm"
  }
]
```

> Sub-chunk results include the original symbol name in `name` as `Symbol[part/total]`. Use `parent_name` (returned in JSON from `--json` mode) to group all parts of a large symbol together.

---

## Configuration

Override default service URLs via environment variables:

```bash
export HAADEX_QDRANT_URL="http://localhost:6333"
export HAADEX_OLLAMA_URL="http://localhost:11434"
```

---

## Adding a new language

1. Get the Tree-sitter grammar:
   ```bash
   go get github.com/smacker/go-tree-sitter/<language>
   ```

2. Add an extractor function in [pkg/engine/parser.go](pkg/engine/parser.go) and register it in `registry`:
   ```go
   var registry = map[string]langConfig{
       // existing...
       "rust": {rust.GetLanguage(), extractRustChunks},
   }
   ```

3. Map the file extension in [pkg/cmd/index.go](pkg/cmd/index.go):
   ```go
   var extByLang = map[string]string{
       // existing...
       ".rs": "rust",
   }
   ```

That's it — no other changes needed.

---

## Project structure

```
haadex/
├── main.go
├── pkg/
│   ├── cmd/
│   │   ├── root.go    cobra root command
│   │   ├── init.go    haadex init
│   │   ├── up.go      haadex up
│   │   ├── index.go   haadex index
│   │   └── query.go   haadex query
│   └── engine/
│       ├── parser.go  Tree-sitter AST extraction (language registry)
│       ├── sqlite.go  SQLite symbol store + FTS5 trigram index
│       ├── qdrant.go  Qdrant gRPC vector upsert/search
│       └── embed.go   Ollama nomic-embed-text HTTP client
└── .haadex/           (generated per project, not committed)
    ├── docker-compose.yml
    ├── symbols.db
    ├── qdrant_storage/
    └── ollama_storage/
```

---

## Persistence

All indexed data survives container restarts. The `.haadex/` directory contains everything:

```bash
# Stop containers for this project
docker compose -f .haadex/docker-compose.yml down

# Resume later — all data is still there
haadex up
haadex query "..."
```

Add `.haadex/` to your `.gitignore` to keep it local:

```gitignore
.haadex/
```
