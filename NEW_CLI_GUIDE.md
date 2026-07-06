# Creating a New Go CLI in the Haadex Style

A simple guide to scaffold a new CLI project that matches the structure used in `haadex`, so diffs stay small when sharing code between projects.

## 1. Project Layout

```
mycli/
├── go.mod
├── main.go
└── pkg/
    └── cmd/
        ├── root.go
        ├── config.go
        └── <subcommand>.go
```

Keep `main.go` at the root, all Cobra commands inside `pkg/cmd/`.

## 2. Initialize the Module

```bash
mkdir mycli && cd mycli
go mod init github.com/<you>/mycli
go get github.com/spf13/cobra@latest
mkdir -p pkg/cmd
```

## 3. `main.go`

Keep it a one-liner — all logic lives in `pkg/cmd`.

```go
package main

import (
	"github.com/<you>/mycli/pkg/cmd"
)

func main() {
	cmd.Execute()
}
```

## 4. `pkg/cmd/root.go`

Defines the root command, version metadata (set via `-ldflags`), and registers subcommands in `init()`.

```go
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Set via -ldflags at build time.
var (
	Version   = "dev"
	CommitSHA = "unknown"
	BuildDate = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "mycli",
	Short: "Short one-liner describing the tool",
	Long:  `Longer description goes here.`,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version, commit, and build date",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("mycli %s (commit %s, built %s)\n", Version, CommitSHA, BuildDate)
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(versionCmd)
}
```

## 5. `pkg/cmd/config.go`

Holds project-local config persisted under a dot-directory (e.g. `.mycli/config.json`).

```go
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Root string `json:"root"`
}

const appDir = ".mycli"

func configPath(root string) string {
	return filepath.Join(root, appDir, "config.json")
}

func loadConfig(root string) (*Config, error) {
	data, err := os.ReadFile(configPath(root))
	if err != nil {
		return nil, fmt.Errorf("config not found — run `mycli init` first: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config.json: %w", err)
	}
	return &cfg, nil
}

func saveConfig(root string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(root), data, 0644)
}
```

## 6. A Subcommand — `pkg/cmd/init.go`

Each subcommand lives in its own file and exposes one `*cobra.Command` variable. Use `RunE` so errors bubble up.

```go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize mycli in the current project",
	RunE:  runInit,
}

func runInit(cmd *cobra.Command, args []string) error {
	if err := os.MkdirAll(appDir, 0755); err != nil {
		return fmt.Errorf("failed to create %s: %w", appDir, err)
	}

	absRoot, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("failed to resolve project root: %w", err)
	}
	cfg := &Config{Root: absRoot}
	if err := saveConfig(".", cfg); err != nil {
		return fmt.Errorf("failed to write config.json: %w", err)
	}

	fmt.Println("✓ Initialized .mycli/")
	fmt.Println("✓ Generated .mycli/config.json")
	return nil
}
```

## 7. Conventions to Keep Diffs Small

- **Package name**: always `cmd` under `pkg/cmd/`.
- **One file per subcommand**: `pkg/cmd/<name>.go`, exported as `var <name>Cmd`.
- **Register in `root.go` `init()`**: every new command added to `rootCmd.AddCommand(...)`.
- **Prefer `RunE`** over `Run`; wrap errors with `fmt.Errorf("…: %w", err)`.
- **Config lives in `.<tool>/`** at the project root, with a `config.json`.
- **Version vars in `root.go`** set via `-ldflags`.
- **Constants next to their use** (e.g. `appDir` in `config.go`).
- **Print success with `✓`** prefix, blank line, then a `Next steps:` block where applicable.

## 8. Build

```bash
go build -o mycli .
./mycli version
./mycli init
```

Optional ldflags build:

```bash
go build -ldflags "\
  -X github.com/<you>/mycli/pkg/cmd.Version=0.1.0 \
  -X github.com/<you>/mycli/pkg/cmd.CommitSHA=$(git rev-parse --short HEAD) \
  -X github.com/<you>/mycli/pkg/cmd.BuildDate=$(date -u +%Y-%m-%d)" \
  -o mycli .
```

## 9. Adding a New Subcommand

1. Create `pkg/cmd/foo.go` with a `var fooCmd = &cobra.Command{...}`.
2. Add `rootCmd.AddCommand(fooCmd)` to `init()` in `root.go`.
3. Use `loadConfig(".")` if the command needs project config.
