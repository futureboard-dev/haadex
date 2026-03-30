package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "haadex",
	Short: "A local-first code indexer with triple-layer retrieval",
	Long: `Haadex indexes your codebase using symbolic, trigram, and semantic layers.
It manages its own Qdrant and Ollama infrastructure via Docker Compose.`,
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
}
