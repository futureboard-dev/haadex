package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func runDockerCompose(composePath string) error {
	run := func(name string, args ...string) error {
		c := exec.Command(name, args...)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	}
	if err := run("docker", "compose", "-f", composePath, "up", "-d"); err != nil {
		// fallback to docker-compose v1
		if err2 := run("docker-compose", "-f", composePath, "up", "-d"); err2 != nil {
			return fmt.Errorf("docker compose up failed: %w", err)
		}
	}
	return nil
}

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Start Qdrant container",
	Long:  `Runs docker-compose up -d to start the Qdrant vector store.`,
	RunE:  runUp,
}

func runUp(cmd *cobra.Command, args []string) error {
	composePath := ".haadex/docker-compose.yml"
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		return fmt.Errorf(".haadex/docker-compose.yml not found — run `haadex init` first")
	}

	fmt.Println("Starting Qdrant container...")
	if err := runDockerCompose(composePath); err != nil {
		return err
	}

	fmt.Println("✓ Qdrant is ready")
	return nil
}

func getQdrantURL() string {
	if v := os.Getenv("HAADEX_QDRANT_URL"); v != "" {
		return v
	}
	return "http://localhost:7333"
}

func getOpenAIKey() string {
	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		return v
	}
	return ""
}
