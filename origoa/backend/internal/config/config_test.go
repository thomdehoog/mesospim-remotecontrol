package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8000" || c.Database == "" || c.Author.Name != "origoa" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if len(c.Scanner.GUIDFiles) == 0 || c.Scanner.GUIDFiles[0] != ".origoa.json" {
		t.Fatalf("scanner default not applied: %+v", c.Scanner)
	}
}

func TestLoadFileThenEnvOverrides(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "origoa.json")
	if err := os.WriteFile(path, []byte(`{"listen":":9000","gitDir":"/from/file","database":"file-dsn"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9000" || c.GitDir != "/from/file" || c.Database != "file-dsn" {
		t.Fatalf("file config not applied: %+v", c)
	}
	// Environment variables override the file.
	t.Setenv("ORIGOA_LISTEN", ":7777")
	t.Setenv("ORIGOA_GIT_DIR", "/from/env")
	t.Setenv("ORIGOA_DSN", "env-dsn")
	t.Setenv("ORIGOA_STATIC", "/static")
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":7777" || c.GitDir != "/from/env" || c.Database != "env-dsn" || c.StaticDir != "/static" {
		t.Fatalf("env overrides not applied: %+v", c)
	}
}

func TestLoadMissingFileErrors(t *testing.T) {
	clearEnv(t)
	if _, err := Load("/no/such/config.json"); err == nil {
		t.Error("expected error for missing config file")
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ORIGOA_LISTEN", "ORIGOA_GIT_DIR", "ORIGOA_DSN", "ORIGOA_STATIC"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}
