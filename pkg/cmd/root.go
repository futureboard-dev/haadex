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
	Use:   "haadex",
	Short: "A code indexer with triple-layer retrieval (symbolic, trigram, semantic)",
	Long: `Haadex indexes your codebase using symbolic, trigram, and semantic layers.
Embedding uses OpenAI (text-embedding-3-large) and enrichment uses OpenRouter.
Qdrant runs locally via Docker Compose.`,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version, commit, and build date",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("haadex %s (commit %s, built %s)\n", Version, CommitSHA, BuildDate)
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(upCmd)
	rootCmd.AddCommand(indexCmd)
	rootCmd.AddCommand(queryCmd)
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(versionCmd)
}
