package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize Haadex in the current project",
	Long:  `Creates the .haadex/ directory and generates docker-compose.yml for Qdrant and Ollama.`,
	RunE:  runInit,
}

const dockerComposeTemplate = `version: "3.9"

services:
  qdrant:
    image: qdrant/qdrant:latest
    container_name: haadex-qdrant
    ports:
      - "6333:6333"
      - "6334:6334"
    volumes:
      - ./qdrant_storage:/qdrant/storage
    networks:
      - haadex-net

  ollama:
    image: ollama/ollama:latest
    container_name: haadex-ollama
    ports:
      - "11434:11434"
    volumes:
      - ./ollama_storage:/root/.ollama
    networks:
      - haadex-net

networks:
  haadex-net:
    driver: bridge
`

func runInit(cmd *cobra.Command, args []string) error {
	haadexDir := ".haadex"

	if err := os.MkdirAll(filepath.Join(haadexDir, "qdrant_storage"), 0755); err != nil {
		return fmt.Errorf("failed to create qdrant_storage dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(haadexDir, "ollama_storage"), 0755); err != nil {
		return fmt.Errorf("failed to create ollama_storage dir: %w", err)
	}

	composePath := filepath.Join(haadexDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(dockerComposeTemplate), 0644); err != nil {
		return fmt.Errorf("failed to write docker-compose.yml: %w", err)
	}

	fmt.Println("✓ Initialized .haadex/")
	fmt.Println("✓ Generated .haadex/docker-compose.yml")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  haadex up      # Start Qdrant and Ollama containers")
	fmt.Println("  haadex index   # Index your codebase")
	return nil
}
