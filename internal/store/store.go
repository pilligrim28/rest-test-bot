// Package store отвечает за хранение профилей пользователей.
//
// Принцип минимизации (ч. 5, 7 ст. 5 152-ФЗ): в файл попадают только те, кто
// явно подписался на материалы и дал согласие на обработку данных. Про
// остальных хранится лишь обезличенный счётчик прохождений.
package store

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Profile — пользователь, который подписался на материалы и дал согласие.
type Profile struct {
	TelegramID int64 `json:"telegram_id"`
	// Username — @логин из профиля Telegram (пусто, если у пользователя его нет).
	Username string `json:"telegram_username,omitempty"`
	// Login — устаревшее поле прежних версий, сохраняется для совместимости.
	Login string `json:"login,omitempty"`
	// FirstName — имя из профиля Telegram.
	FirstName string `json:"first_name,omitempty"`
	// Phone — номер, которым пользователь сам поделился кнопкой Telegram.
	Phone string `json:"phone,omitempty"`

	// ConsentedAt — когда дано согласие на обработку ПДн.
	ConsentedAt time.Time `json:"consented_at"`
	// ConsentVersion — редакция текста согласия.
	ConsentVersion string `json:"consent_version,omitempty"`
	// ConsentMethod — как выражено согласие (для доказательства, ч. 3 ст. 9 152-ФЗ).
	ConsentMethod string `json:"consent_method,omitempty"`
	// MarketingConsentAt — когда пользователь согласился получать рассылку.
	MarketingConsentAt time.Time `json:"marketing_consent_at"`
	// UpdatedAt — когда профиль последний раз менялся.
	UpdatedAt time.Time `json:"updated_at"`

	// Scores — баллы по категориям за последнее прохождение.
	Scores map[string]int `json:"scores,omitempty"`
	// Headline — название привычки, показанное пользователю.
	Headline string `json:"headline,omitempty"`
	// Runs — сколько раз пользователь сохранял результат.
	Runs int `json:"runs"`
}

// Subscribed сообщает, что пользователь согласился получать рассылку.
func (p Profile) Subscribed() bool { return !p.MarketingConsentAt.IsZero() }

// Store — потокобезопасное JSON-хранилище профилей.
type Store struct {
	mu       sync.RWMutex
	path     string
	profiles map[int64]*Profile
	// testsCompleted — обезличенный счётчик завершённых тестов.
	testsCompleted int
}

// New создаёт хранилище и загружает существующий файл, если он есть.
func New(path string) (*Store, error) {
	s := &Store{path: path, profiles: make(map[int64]*Profile)}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Save создаёт или обновляет профиль и сохраняет файл на диск.
func (s *Store) Save(p Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	p.UpdatedAt = now
	if p.ConsentedAt.IsZero() {
		p.ConsentedAt = now
	}
	if existing, ok := s.profiles[p.TelegramID]; ok {
		p.Runs = existing.Runs + 1
		if p.Phone == "" {
			p.Phone = existing.Phone
		}
	} else {
		p.Runs = 1
	}
	s.profiles[p.TelegramID] = &p
	return s.persistLocked()
}

// IncTests увеличивает обезличенный счётчик завершённых тестов.
func (s *Store) IncTests() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.testsCompleted++
	if err := s.persistLocked(); err != nil {
		log.Printf("[store] не удалось сохранить счётчик тестов: %v", err)
	}
}

// UpdateResult обновляет результат теста у существующего подписчика,
// не трогая данные согласия. Возвращает false, если профиля нет.
func (s *Store) UpdateResult(id int64, scores map[string]int, headline string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[id]
	if !ok {
		return false, nil
	}
	p.Scores = scores
	p.Headline = headline
	p.Runs++
	p.UpdatedAt = time.Now().UTC()
	return true, s.persistLocked()
}

// TestsCompleted возвращает число завершённых тестов (без привязки к людям).
func (s *Store) TestsCompleted() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.testsCompleted
}

// Get возвращает копию профиля по Telegram ID.
func (s *Store) Get(id int64) (Profile, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[id]
	if !ok {
		return Profile{}, false
	}
	return *p, true
}

// Count возвращает число профилей.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.profiles)
}

// Delete удаляет профиль (отзыв согласия, ст. 21 152-ФЗ).
func (s *Store) Delete(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.profiles[id]; !ok {
		return nil
	}
	delete(s.profiles, id)
	return s.persistLocked()
}

// Subscribers — ID тех, кто согласился и на обработку данных, и на рассылку.
func (s *Store) Subscribers() []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]int64, 0, len(s.profiles))
	for id, p := range s.profiles {
		if p.Subscribed() {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// List возвращает профили, отсортированные по времени согласия.
func (s *Store) List() []Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConsentedAt.Before(out[j].ConsentedAt) })
	return out
}

// Purge удаляет профили, не обновлявшиеся дольше maxAge. Возвращает число удалённых.
func (s *Store) Purge(maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().UTC().Add(-maxAge)
	removed := 0
	for id, p := range s.profiles {
		if p.UpdatedAt.Before(cutoff) {
			delete(s.profiles, id)
			removed++
		}
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, s.persistLocked()
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	var payload fileFormat
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	for id, p := range payload.Profiles {
		if p == nil {
			continue
		}
		// Профили старых версий: прежний текст согласия включал рассылку,
		// поэтому считаем дату согласия и датой подписки.
		if p.MarketingConsentAt.IsZero() && p.ConsentVersion == "" {
			p.MarketingConsentAt = p.ConsentedAt
			p.ConsentVersion = "legacy"
		}
		if p.Username == "" && p.Login != "" {
			p.Username = p.Login
		}
		s.profiles[id] = p
	}
	s.testsCompleted = payload.TestsCompleted
	// Поле opted_in из старых версий (ID отказавшихся) намеренно не читается:
	// при следующей записи оно исчезнет из файла.
	return nil
}

// persistLocked пишет файл атомарно: сначала во временный, потом rename.
func (s *Store) persistLocked() error {
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	payload := fileFormat{
		UpdatedAt:      time.Now().UTC(),
		TestsCompleted: s.testsCompleted,
		Profiles:       s.profiles,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

type fileFormat struct {
	UpdatedAt      time.Time          `json:"updated_at"`
	TestsCompleted int                `json:"tests_completed"`
	Profiles       map[int64]*Profile `json:"profiles"`
}
