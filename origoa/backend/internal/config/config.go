// Package config loads the Origoa server configuration.
package config

import (
	"encoding/json"
	"fmt"
	"os"

	"origoa/internal/scanner"
)

// Config is the server configuration (origoa.json).
type Config struct {
	Listen    string `json:"listen"`
	GitDir    string `json:"gitDir"`
	Database  string `json:"database"`
	StaticDir string `json:"staticDir"`
	Author    struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"author"`
	Scanner scanner.Config `json:"scanner"`
}

// Load reads the configuration file (optional) and applies environment
// overrides (ORIGOA_LISTEN, ORIGOA_GIT_DIR, ORIGOA_DSN, ORIGOA_STATIC).
func Load(path string) (Config, error) {
	c := Config{
		Listen:   ":8000",
		GitDir:   "./data/repo.git",
		Database: "postgres://origoa:origoa@localhost:5432/origoa",
		Scanner:  scanner.DefaultConfig(),
	}
	c.Author.Name = "origoa"
	c.Author.Email = "origoa@localhost"
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return c, fmt.Errorf("config: %w", err)
		}
		if err := json.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("config %s: %w", path, err)
		}
	}
	if v := os.Getenv("ORIGOA_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("ORIGOA_GIT_DIR"); v != "" {
		c.GitDir = v
	}
	if v := os.Getenv("ORIGOA_DSN"); v != "" {
		c.Database = v
	}
	if v := os.Getenv("ORIGOA_STATIC"); v != "" {
		c.StaticDir = v
	}
	return c, nil
}
