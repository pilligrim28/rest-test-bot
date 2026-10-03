package store

import (
	"path/filepath"
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

func TestOptInAndRecipients(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	s.MarkOptIn(100) // прошёл тест, отказался от обработки
	s.MarkOptIn(200) // прошёл тест, отказался
	if err := s.Save(Profile{TelegramID: 100, Login: "@a"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if got := len(s.Recipients()); got != 2 {
		t.Errorf("Recipients = %d, want 2", got)
	}
	if got := len(s.ConsentedRecipients()); got != 1 {
		t.Errorf("ConsentedRecipients = %d, want 1", got)
	}
	if s.Count() != 1 {
		t.Errorf("Count = %d, want 1", s.Count())
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
