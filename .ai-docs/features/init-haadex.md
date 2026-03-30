To make Haadex truly "plug-and-play," we will update the haadex init command. When a user runs it, Haadex will generate a high-performance Docker Compose file inside the .haadex directory.

This ensures that Qdrant (the vector brain) and Ollama (the embedding heart) are always configured correctly without the user having to install them manually on their host OS.

Agent Task Spec: Haadex — High-Performance Local Code Indexer
1. Task

Build Haadex, a portable Go-based CLI tool that creates a local-first index of any codebase. Haadex must manage its own infrastructure (Qdrant and Ollama) via Docker Compose and implement a triple-layer retrieval system (Symbolic, Trigram, and Semantic) for AI Agents.

2. Files In Scope

CREATE:

main.go: CLI entry point using spf13/cobra.

pkg/cmd/init.go: Logic for haadex init. Must generate .haadex/docker-compose.yml.

pkg/cmd/up.go: Logic for haadex up (helper to run docker-compose up -d).

pkg/cmd/index.go: Logic for haadex index (AST parsing, hashing, and embedding).

pkg/cmd/query.go: Logic for haadex query (Hybrid search).

pkg/engine/parser.go: Tree-sitter integration (Go, TS, TSX).

pkg/engine/sqlite.go: SQLite Symbols + Trigram FTS5.

pkg/engine/qdrant.go: Qdrant Go SDK integration (connecting to Docker service).

pkg/engine/embed.go: Nomic v1.5 client (connecting to Ollama Docker service).

3. Infrastructure (Docker Compose)

The haadex init command must generate a .haadex/docker-compose.yml with these services:

Qdrant: qdrant/qdrant:latest, Port 6333, mapped to ./.haadex/qdrant_storage.

Ollama: ollama/ollama:latest, Port 11434, mapped to ./.haadex/ollama_storage.

Network: Both services must be on a shared bridge network so the Haadex CLI can reach them.

4. CLI Commands to Implement

haadex init: Initializes the .haadex/ directory and writes the docker-compose.yml.

haadex up: Automatically runs docker-compose -f .haadex/docker-compose.yml up -d and ensures nomic-embed-text is pulled into Ollama.

haadex index:

Scans the project (respecting .gitignore).

Extracts symbols via Tree-sitter.

Generates Nomic embeddings via the Ollama container.

Upserts to the Qdrant container.

haadex query "<query>":

Hybrid search (Symbolic -> Literal -> Semantic).

Returns JSON results for AI tool-calling.

5. Done Criteria

One-Command Setup: haadex init && haadex up results in a fully functional indexing environment.

Persistence: All indexed data and downloaded models survive a docker-compose down (stored in .haadex/).

Automatic Model Pulling: Haadex ensures ollama pull nomic-embed-text is executed before the first index.

Matryoshka Vectors: Truncate Nomic vectors to 512 dimensions in Qdrant for memory efficiency.

FTS5 Trigram: SQLite correctly indexes raw code for exact substring matching.

Symbolic Extraction: Tree-sitter successfully extracts Go structs/methods and TypeScript interfaces/classes.

6. Explicit Decisions (Do not infer)

Folder Name: All Haadex assets MUST live in ./.haadex/.

Docker Dependency: Haadex will assume the user has Docker installed.

Qdrant Connection: CLI connects to Qdrant via localhost:6333.

Ollama Connection: CLI connects to Ollama via localhost:11434.

Embedding Prefix: Prepend search_document: for indexing and search_query: for queries.

Chunking: Index by Logical AST Boundaries (functions/classes).

7. Environment Variables Needed
code
Bash
download
content_copy
expand_less
HAADEX_QDRANT_URL="http://localhost:6444"
HAADEX_OLLAMA_URL="http://localhost:11435"
8. Context: The "Infrastructure-as-Project" Approach

By putting the docker-compose.yml inside the project's .haadex folder:

Isolation: A user can stop the containers for Project A and start them for Project B to save system resources.

Portability: The entire indexing environment (database, vector store, and AI model) is localized to the repo.

Low Friction: The developer doesn't need to know how to configure Qdrant or Ollama; Haadex manages the "shit we need" via the Compose template.

Ready to hand this off to the Builder AI? (It will now build a tool that manages its own stack).