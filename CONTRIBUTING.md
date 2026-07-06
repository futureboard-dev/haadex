# Contributing to Haadex

Thanks for your interest in improving Haadex! Contributions of all kinds are
welcome — bug reports, features, docs, and tests.

## Getting started

1. Fork the repo and clone your fork.
2. Make sure you have **Go 1.25+** and **Docker** (for the Qdrant instance)
   installed.
3. Build the binary:

   ```bash
   make build
   ```

4. Bring up the local Qdrant infrastructure when running end-to-end:

   ```bash
   make up
   ```

## Making changes

- Create a feature branch: `git checkout -b feature/short-description`
- Keep changes focused — one logical change per pull request.
- Match the style of the surrounding code.

## Before you open a pull request

Please make sure all of the following pass:

```bash
go build ./...   # compiles cleanly
go vet ./...     # no vet warnings
go test ./...    # all tests green
```

New behavior should come with tests where practical.

## Commit messages

- Use clear, imperative subject lines (e.g. "Add trigram fallback for X").
- Reference related issues in the body where relevant.

## Reporting bugs

Open an issue with:

- What you expected vs. what happened
- Steps to reproduce
- Your OS, Go version, and Haadex commit SHA

For **security** issues, do not open a public issue — see
[SECURITY.md](SECURITY.md).

## Code of Conduct

By participating, you agree to abide by our
[Code of Conduct](CODE_OF_CONDUCT.md).
