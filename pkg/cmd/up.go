package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

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
	Short: "Start Qdrant and Ollama containers",
	Long:  `Runs docker-compose up -d and ensures nomic-embed-text model is pulled into Ollama.`,
	RunE:  runUp,
}

func runUp(cmd *cobra.Command, args []string) error {
	composePath := ".haadex/docker-compose.yml"
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		return fmt.Errorf(".haadex/docker-compose.yml not found — run `haadex init` first")
	}

	fmt.Println("Starting containers (this may take a few minutes on first run to download images)...")
	if err := runDockerCompose(composePath); err != nil {
		return err
	}

	ollamaURL := getOllamaURL()
	fmt.Printf("Waiting for Ollama at %s ...\n", ollamaURL)
	if err := waitForOllama(ollamaURL, 5*time.Minute); err != nil {
		return fmt.Errorf("Ollama did not become ready: %w", err)
	}

	fmt.Println("Pulling nomic-embed-text model (this may take a while on first run)...")
	if err := pullOllamaModel(ollamaURL, "nomic-embed-text"); err != nil {
		return fmt.Errorf("failed to pull nomic-embed-text: %w", err)
	}

	fmt.Println("✓ Haadex infrastructure is ready")
	return nil
}

func waitForOllama(baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/tags")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout after %s", timeout)
}

func pullOllamaModel(baseURL, model string) error {
	payload := map[string]string{"name": model}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(baseURL+"/api/pull", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	return nil
}

func getOllamaURL() string {
	if v := os.Getenv("HAADEX_OLLAMA_URL"); v != "" {
		return v
	}
	return "http://localhost:11434"
}

func getQdrantURL() string {
	if v := os.Getenv("HAADEX_QDRANT_URL"); v != "" {
		return v
	}
	return "http://localhost:6333"
}
