package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadRequiresToken(t *testing.T) {
	t.Setenv(envToken, "")
	t.Setenv(envTokenAlt, "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() = nil error, want error about missing token")
	}
}

func TestLoadParsesAdmins(t *testing.T) {
	t.Setenv(envToken, "123:ABC")
	t.Setenv(envAdmins, "42, 100 ,7")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AdminCount() != 3 {
		t.Fatalf("AdminCount = %d, want 3", cfg.AdminCount())
	}
	for _, id := range []int64{42, 100, 7} {
		if !cfg.IsAdmin(id) {
			t.Errorf("IsAdmin(%d) = false, want true", id)
		}
	}
	if cfg.IsAdmin(999) {
		t.Error("IsAdmin(999) = true, want false")
	}
}

func TestLoadRejectsBadAdminID(t *testing.T) {
	t.Setenv(envToken, "123:ABC")
	t.Setenv(envAdmins, "not-a-number")

	if _, err := Load(); err == nil {
		t.Fatal("Load() = nil error, want parse error")
	}
}

func TestLoadAcceptsLegacyTokenName(t *testing.T) {
	t.Setenv(envToken, "")
	t.Setenv(envTokenAlt, "legacy-token")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != "legacy-token" {
		t.Errorf("Token = %q, want legacy-token", cfg.Token)
	}
}

func TestBroadcastDelayFloor(t *testing.T) {
	t.Setenv(envToken, "123:ABC")
	t.Setenv(envAdmins, "")
	t.Setenv(envDelayMS, "1") // слишком быстро для Telegram

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BroadcastDelay < 50*time.Millisecond {
		t.Errorf("BroadcastDelay = %v, want at least 50ms", cfg.BroadcastDelay)
	}
}

func TestDataFileDefault(t *testing.T) {
	t.Setenv(envToken, "123:ABC")
	t.Setenv(envDataFile, "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DataFile != defaultDataFile {
		t.Errorf("DataFile = %q, want %q", cfg.DataFile, defaultDataFile)
	}
}

func TestPracticeURLFromEnv(t *testing.T) {
	t.Setenv("BOT_TOKEN", "x")
	t.Setenv("PRACTICE_URL", "https://my-practice.example/audio")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PracticeURL != "https://my-practice.example/audio" {
		t.Errorf("PracticeURL = %q", cfg.PracticeURL)
	}
}

func TestPracticeURLEmptyByDefault(t *testing.T) {
	t.Setenv("BOT_TOKEN", "x")
	t.Setenv("PRACTICE_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PracticeURL != "" {
		t.Errorf("ожидал пусто, получено %q", cfg.PracticeURL)
	}
}

func TestLoadBotGateWithoutToken(t *testing.T) {
	t.Setenv(envToken, "")
	t.Setenv(envTokenAlt, "")
	t.Setenv("BOTGATE_API_KEY", "bg_live_test")
	t.Setenv("BOTGATE_BOT_ID", "")
	t.Setenv("BOTGATE_WEBHOOK_SECRET", "s")
	if _, err := Load(); err == nil {
		t.Fatal("без BOTGATE_BOT_ID должна быть ошибка")
	}
	t.Setenv("BOTGATE_BOT_ID", "bot_abc")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("BotGate без BOT_TOKEN должен запускаться: %v", err)
	}
	if !cfg.UseBotGate() || cfg.Validate() != nil {
		t.Fatal("ожидал режим BotGate")
	}
}

func TestDotEnvWithBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("\xEF\xBB\xBF\r\nBOM_TEST_TOKEN=1:A\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOM_TEST_TOKEN", "")
	if !applyDotEnv(path) || os.Getenv("BOM_TEST_TOKEN") != "1:A" {
		t.Fatalf(".env с BOM не прочитан: %q", os.Getenv("BOM_TEST_TOKEN"))
	}
}
