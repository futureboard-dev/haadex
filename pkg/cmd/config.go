package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ModelConfig describes a model provider and authentication.
type ModelConfig struct {
	Provider string `json:"provider"` // "openai" or "openrouter" or other provider name
	APIKey   string `json:"api_key"`  // env var name to read the key from (e.g., "OPENAI_API_KEY")
	BaseURL  string `json:"base_url,omitempty"` // optional custom base URL
	Model    string `json:"model"`    // model identifier (e.g., "gpt-4o-mini" or "deepseek/deepseek-chat")
}

// HaadexConfig is stored in .haadex/config.json to anchor a project's index.
type HaadexConfig struct {
	Root       string       `json:"root"`
	Collection string       `json:"collection"`
	IndexedAt  *time.Time   `json:"indexed_at,omitempty"`
	Enrichment *ModelConfig `json:"enrichment,omitempty"`
	Embedding  *ModelConfig `json:"embedding,omitempty"`
}

const haadexDir = ".haadex"

func configPath(root string) string {
	return filepath.Join(root, haadexDir, "config.json")
}

// loadConfig reads .haadex/config.json relative to the given root.
func loadConfig(root string) (*HaadexConfig, error) {
	data, err := os.ReadFile(configPath(root))
	if err != nil {
		return nil, fmt.Errorf("config not found — run `haadex init` first: %w", err)
	}
	var cfg HaadexConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config.json: %w", err)
	}
	return &cfg, nil
}

// saveConfig writes the config back to .haadex/config.json.
func saveConfig(root string, cfg *HaadexConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(root), data, 0644)
}

// deriveCollection returns a per-project Qdrant collection name based on the
// absolute path of the project root. This prevents cross-project index pollution.
func deriveCollection(absRoot string) string {
	h := sha256.Sum256([]byte(absRoot))
	return "haadex_" + fmt.Sprintf("%x", h[:6])
}

// getModelConfig loads a ModelConfig and resolves the API key from the environment.
// Returns error if the config is missing required fields or the API key env var is not set.
func getModelConfig(mc *ModelConfig, purpose string) (provider, apiKey, baseURL, model string, err error) {
	if mc == nil {
		return "", "", "", "", fmt.Errorf("%s model not configured in .haadex/config.json", purpose)
	}
	if mc.Provider == "" {
		return "", "", "", "", fmt.Errorf("%s model config missing provider", purpose)
	}
	if mc.APIKey == "" {
		return "", "", "", "", fmt.Errorf("%s model config missing api_key env var name", purpose)
	}
	if mc.Model == "" {
		return "", "", "", "", fmt.Errorf("%s model config missing model name", purpose)
	}

	apiKeyValue := os.Getenv(mc.APIKey)
	if apiKeyValue == "" {
		return "", "", "", "", fmt.Errorf("%s API key not set: env var %s is empty", purpose, mc.APIKey)
	}

	resolvedBaseURL := mc.BaseURL
	if resolvedBaseURL == "" {
		// Default base URLs for known providers
		switch mc.Provider {
		case "openai":
			resolvedBaseURL = "https://api.openai.com"
		case "openrouter":
			resolvedBaseURL = "https://openrouter.ai/api/v1"
		default:
			return "", "", "", "", fmt.Errorf("unknown provider %q for %s; add base_url to config", mc.Provider, purpose)
		}
	}

	return mc.Provider, apiKeyValue, resolvedBaseURL, mc.Model, nil
}

func getEnrichmentKey() string { return os.Getenv("OPENROUTER_API_KEY") }
