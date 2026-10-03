package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvFromENVFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("BOT_TOKEN=999:DOTENV\nADMIN_IDS=7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", filepath.Join(dir, ".env"))
	t.Setenv(envToken, "")
	t.Setenv(envTokenAlt, "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != "999:DOTENV" {
		t.Errorf("Token = %q, want token from .env", cfg.Token)
	}
	if !cfg.IsAdmin(7) {
		t.Error("IsAdmin(7) = false, want true (ADMIN_IDS из .env)")
	}
}

func TestLoadDotEnvFromCurrentDir(t *testing.T) {
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(prev)

	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".env", []byte("BOT_TOKEN=111:LOCALDOTENV\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", "")
	t.Setenv(envToken, "")
	t.Setenv(envTokenAlt, "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != "111:LOCALDOTENV" {
		t.Errorf("Token = %q, want token from .env в текущей директории", cfg.Token)
	}
}
