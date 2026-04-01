package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// HaadexConfig is stored in .haadex/config.json to anchor a project's index.
type HaadexConfig struct {
	Root       string    `json:"root"`
	Collection string    `json:"collection"`
	IndexedAt  *time.Time `json:"indexed_at,omitempty"`
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
