# Haadex

A portable, local-first code indexer for AI agents. Haadex manages its own Qdrant infrastructure via Docker Compose and implements a **triple-layer retrieval system** so AI tools get precise, contextual answers about any codebase.

## How it works

```
haadex index  →  Tree-sitter AST  →  SQLite (symbols + trigram FTS5)
                                  →  Qdrant  (3072-dim OpenAI vectors)

haadex query  →  Layer 1: Symbolic  (exact/partial name match in SQLite)
              →  Layer 2: Trigram   (FTS5 substring search in SQLite)
              →  Layer 3: Semantic  (vector nearest-neighbour in Qdrant)
              →  Cross-layer ranking (score normalization + path boosting)
              →  JSON results
```

All data (vector store, SQLite DB) lives inside `.haadex/` — one directory per project, fully isolated.

---

## Prerequisites

- **Docker** (with Compose v2 or docker-compose v1)
- **Go 1.21+** (to build from source)
- **OpenAI API key** (for `text-embedding-3-large` embeddings)

---

## Build & install

```bash
git clone https://github.com/haadex/haadex
cd haadex
go build -ldflags "-X github.com/haadex/haadex/pkg/cmd.Version=1.1.0 \
  -X github.com/haadex/haadex/pkg/cmd.CommitSHA=$(git rev-parse --short HEAD) \
  -X github.com/haadex/haadex/pkg/cmd.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o ~/.local/bin/haadex .

# Verify
haadex version
```

### Updating after source changes

After editing haadex source code, rebuild and reinstall so all projects pick up the new binary:

```bash
cd /path/to/haadex
go build -ldflags "-X github.com/haadex/haadex/pkg/cmd.Version=1.1.0 \
  -X github.com/haadex/haadex/pkg/cmd.CommitSHA=$(git rev-parse --short HEAD) \
  -X github.com/haadex/haadex/pkg/cmd.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o ~/.local/bin/haadex .
```

Verify the other project is using the latest build:

```bash
haadex version
# haadex 1.1.0 (commit a3f1b2c, built 2026-04-02T10:30:00Z)
```

If you have Claude Code running in another project, **restart Claude Code** — the MCP server is a long-lived child process that runs the old binary until restarted.

---

## Quick start

```bash
# 0. Set your OpenAI API key
export OPENAI_API_KEY="sk-..."

# 1. Go to your project
cd /path/to/your/project

# 2. Initialize Haadex (creates .haadex/ and docker-compose.yml)
haadex init

# 3. Start Qdrant container
haadex up

# 4. Index the codebase
haadex index

# 5. Query
haadex query "authentication middleware"
haadex query "database connection pool" --json
```

---

## Using with Claude Code (MCP)

Haadex includes a built-in MCP server that exposes `search_code` and `index_dir` as tools for Claude Code.

### 1. Register the MCP server

```bash
# From your project directory (uses project-scoped config)
claude mcp add haadex -s local -- ~/.local/bin/haadex mcp
```

Or register globally (available in all projects):

```bash
claude mcp add haadex -s user -- ~/.local/bin/haadex mcp
```

### 2. Enable the tools

Open Claude Code and type `/mcp` to open the MCP dialog. Toggle **haadex** on.

### 3. Add a CLAUDE.md to your project

Copy the included template to tell Claude when and how to use the tools:

```bash
cp /path/to/haadex/CLAUDE.md.template /path/to/your/project/CLAUDE.md
```

Or add this to your existing `CLAUDE.md`:

```markdown
# Haadex — Code Search

This project is indexed with [haadex](https://github.com/haadex/haadex).
The `search_code` and `index_dir` MCP tools are available.

## When to use `search_code`
- Before reading or editing any unfamiliar file — search first, then read
- When asked where something is implemented
- Before adding new code — check if something similar already exists
- When you need to understand how a type/function is used across the codebase

## When to use `index_dir`
- First session on this project (run once): `index_dir` with path `.`
- After large refactors or many new files are added

## Rules
- Prefer `search_code` over Grep/Glob for semantic questions
- Use Grep/Glob only for exact patterns or file structure exploration
- If `search_code` returns 0 results, fall back to Grep
```

### 4. Environment variables

The MCP server inherits environment variables from the Claude Code process. Make sure `OPENAI_API_KEY` is set in your shell **before** launching Claude Code:

```bash
# Add to ~/.zshrc or ~/.bashrc
export OPENAI_API_KEY="sk-..."
```

If you change the key, you must **restart Claude Code** for the MCP server to pick it up.

### MCP tools

| Tool | Description |
|------|-------------|
| `search_code` | Hybrid search across all three layers. Pass a `query` string and optional `limit` (default 5). |
| `index_dir` | Index a directory. Pass a `path` (default: current directory). |

---

## Commands

### `haadex init`

Creates `.haadex/` in the current directory with:

```
.haadex/
├── docker-compose.yml   <- Qdrant service
├── config.json          <- project config (collection name, root path)
└── qdrant_storage/      <- vector DB data (persisted)
```

### `haadex up`

Starts Qdrant via Docker Compose.

```bash
haadex up
```

Services exposed on localhost:
| Service | Port  |
|---------|-------|
| Qdrant REST | 6333 |
| Qdrant gRPC | 6334 |

### `haadex index [path]`

Walks the project directory (respecting `.gitignore`), extracts symbols via Tree-sitter, generates embeddings via OpenAI, and upserts into both SQLite and Qdrant.

```bash
haadex index           # index current directory
haadex index ./src     # index a subdirectory
```

Incremental by default — only new or changed files are re-indexed.

**Supported languages:**

| Language   | Extensions     |
|------------|----------------|
| Go         | `.go`          |
| JavaScript | `.js`, `.jsx`  |
| TypeScript | `.ts`          |
| TSX        | `.tsx`         |
| Python     | `.py`          |

**What gets extracted:**

- Go: `function`, `method`, `struct`, `interface`, `type`
- JavaScript/TypeScript/TSX: `function`, `arrow function`, `variable` (exported consts, config objects), `class`, `interface`, `type`
- Python: `function`, `class`

**Embedding details:**
- Model: `text-embedding-3-large` (via OpenAI API)
- Dimensions: **3072**
- Embeddings are enriched with file path and symbol metadata for better semantic retrieval
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
    "layers":  ["symbolic", "trigram"],
    "file":    "pkg/server/handler.go",
    "name":    "AuthMiddleware",
    "kind":    "function",
    "line":    42,
    "score":   1.1,
    "snippet": "func AuthMiddleware(next http.Handler) http.Handler {..."
  },
  {
    "layers":  ["semantic"],
    "file":    "src/app/dashboard/CreateConsultationForm.tsx",
    "name":    "CreateConsultationForm[2/3]",
    "kind":    "function",
    "line":    1,
    "score":   0.6912,
    "snippet": "..."
  }
]
```

Results are ranked by a unified score across all layers. Results found by multiple layers get a bonus, so multi-layer agreement surfaces the most relevant hits first.

> Sub-chunk results include the original symbol name in `name` as `Symbol[part/total]`.

### `haadex mcp`

Starts the MCP server over stdin/stdout (JSON-RPC). Not intended to be run directly — used by Claude Code as a stdio transport. See [Using with Claude Code](#using-with-claude-code-mcp) above.

---

## Configuration

Override default service URLs via environment variables:

```bash
export HAADEX_QDRANT_URL="http://localhost:6333"   # default
export OPENAI_API_KEY="sk-..."                      # required
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
│   │   ├── query.go   haadex query
│   │   └── mcp.go     haadex mcp (MCP server)
│   └── engine/
│       ├── parser.go  Tree-sitter AST extraction (language registry)
│       ├── sqlite.go  SQLite symbol store + FTS5 trigram index
│       ├── qdrant.go  Qdrant gRPC vector upsert/search
│       ├── embed.go   OpenAI text-embedding-3-large client
│       └── ranker.go  Cross-layer score normalization and ranking
└── .haadex/           (generated per project, not committed)
    ├── docker-compose.yml
    ├── config.json
    ├── symbols.db
    └── qdrant_storage/
```

---

## Persistence

All indexed data survives container restarts. The `.haadex/` directory contains everything:

```bash
# Stop Qdrant for this project
docker compose -f .haadex/docker-compose.yml down

# Resume later — all data is still there
haadex up
haadex query "..."
```

Add `.haadex/` to your `.gitignore` to keep it local:

```gitignore
.haadex/
```
