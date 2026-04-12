package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Addr   string `yaml:"addr"`
	APIKey string `yaml:"api_key"`
	DBPath string `yaml:"db_path"`
}

// Load reads server config from path. Missing file returns defaults;
// parse errors are returned as errors.
func Load(path string) (*Config, error) {
	cfg := &Config{
		Addr:   ":7777",
		DBPath: filepath.Join(os.Getenv("HOME"), ".gru", "gru.db"),
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg.APIKey = generateKey()
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	if cfg.APIKey == "" {
		cfg.APIKey = generateKey()
	}

	return cfg, nil
}

func generateKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("config: failed to generate API key: " + err.Error())
	}
	return hex.EncodeToString(b)
}
