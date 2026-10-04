package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")

	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	p := Profile{
		TelegramID:  42,
		Username:    "tester",
		Login:       "@tester",
		ConsentedAt: time.Now().UTC(),
		Scores:      map[string]int{"A": 3, "B": 1, "C": 1},
		Headline:    "Рабочий день закончился, а голова продолжает работать",
	}
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Второе сохранение того же пользователя увеличивает счётчик прохождений.
	p.Login = "@tester2"
	if err := s.Save(p); err != nil {
		t.Fatalf("Save twice: %v", err)
	}

	reopened, err := New(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}

	got, ok := reopened.Get(42)
	if !ok {
		t.Fatal("profile not found after reload")
	}
	if got.Login != "@tester2" {
		t.Errorf("login = %q, want @tester2", got.Login)
	}
	if got.Runs != 2 {
		t.Errorf("runs = %d, want 2", got.Runs)
	}
	if got.Scores["A"] != 3 {
		t.Errorf("scores[A] = %d, want 3", got.Scores["A"])
	}
}

func TestSubscribersAndCounter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.IncTests()
	s.IncTests()
	now := time.Now().UTC()
	if err := s.Save(Profile{TelegramID: 100, ConsentVersion: "v", MarketingConsentAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(Profile{TelegramID: 200, ConsentVersion: "v"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Subscribers(); len(got) != 1 || got[0] != 100 {
		t.Errorf("Subscribers = %v, want [100]", got)
	}
	reopened, _ := New(path)
	if reopened.TestsCompleted() != 2 {
		t.Errorf("TestsCompleted = %d, want 2", reopened.TestsCompleted())
	}
}

func TestLegacyFileMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	legacy := `{"profiles":{"5":{"telegram_id":5,"login":"@old","consented_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z"}},"opted_in":[5,6,7]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := s.Get(5)
	if !ok || !p.Subscribed() || p.Username != "@old" {
		t.Fatalf("старый профиль не перенесён: %+v", p)
	}
	s.IncTests() // перезапись файла
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "opted_in") {
		t.Error("ID отказавшихся (opted_in) не должны оставаться в файле")
	}
}

func TestPurge(t *testing.T) {
	s, _ := New(filepath.Join(t.TempDir(), "users.json"))
	_ = s.Save(Profile{TelegramID: 1})
	if n, _ := s.Purge(time.Hour); n != 0 {
		t.Errorf("свежий профиль удалён")
	}
	s.profiles[1].UpdatedAt = time.Now().Add(-2 * time.Hour)
	if n, _ := s.Purge(time.Hour); n != 1 || s.Count() != 0 {
		t.Errorf("просроченный профиль не удалён")
	}
}

func TestLoadMissingFile(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("New on missing file: %v", err)
	}
	if s.Count() != 0 {
		t.Errorf("Count = %d, want 0", s.Count())
	}
}
