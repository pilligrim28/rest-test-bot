// Package store отвечает за хранение профилей пользователей.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Profile — согласившийся на обработку данных пользователь.
//
// Профиль создаётся только после явного согласия: до нажатия
// кнопки «Согласен» в файл не попадает ничего.
type Profile struct {
	TelegramID int64  `json:"telegram_id"`
	Username   string `json:"telegram_username"`
	// Login — логин в Telegram, который пользователь подтвердил сам.
	Login string `json:"login"`
	// FirstName — отображаемое имя из Telegram (необязательное поле).
	FirstName string `json:"first_name,omitempty"`
	// ConsentedAt — когда пользователь дал согласие на обработку данных.
	ConsentedAt time.Time `json:"consented_at"`
	// UpdatedAt — когда профиль последний раз менялся.
	UpdatedAt time.Time `json:"updated_at"`

	// Scores — баллы по категориям за последнее прохождение.
	Scores map[string]int `json:"scores"`
	// Headline — название привычки, показанное пользователю.
	Headline string `json:"headline"`
	// Runs — сколько раз пользователь прошёл тест.
	Runs int `json:"runs"`
}

// Store — потокобезопасное JSON-хранилище профилей.
type Store struct {
	mu       sync.RWMutex
	path     string
	profiles map[int64]*Profile
	// optIn — все, кто проходил тест (включая отказавшихся), нужен для списка админа.
	optIn map[int64]bool
}

// New создаёт хранилище и загружает существующий файл, если он есть.
func New(path string) (*Store, error) {
	s := &Store{
		path:     path,
		profiles: make(map[int64]*Profile),
		optIn:    make(map[int64]bool),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Save перезаписывает профиль после согласия и сохраняет файл на диск.
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
	} else {
		p.Runs = 1
	}

	s.profiles[p.TelegramID] = &p
	s.optIn[p.TelegramID] = true

	return s.persistLocked()
}

// MarkOptIn отмечает пользователя как прошедшего тест без согласия на обработку данных.
func (s *Store) MarkOptIn(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.optIn[id] = true
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

// Count возвращает число профилей с согласием.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.profiles)
}

// Delete удаляет профиль и отметку участия (отзыв согласия).
func (s *Store) Delete(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.profiles, id)
	delete(s.optIn, id)
	return s.persistLocked()
}

// Recipients — ID всех, кто прошёл тест (для массовой рассылки).
func (s *Store) Recipients() []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := make([]int64, 0, len(s.optIn))
	for id := range s.optIn {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// ConsentedRecipients — ID только тех, кто дал согласие на обработку данных.
func (s *Store) ConsentedRecipients() []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := make([]int64, 0, len(s.profiles))
	for id := range s.profiles {
		ids = append(ids, id)
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

// load читает JSON-файл с диска.
func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // первый запуск — файла ещё нет
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
		if p.Scores == nil {
			p.Scores = map[string]int{}
		}
		s.profiles[id] = p
		s.optIn[id] = true
	}
	for _, id := range payload.OptedIn {
		s.optIn[id] = true
	}
	return nil
}

// persistLocked пишет файл атомарно: сначала во временный, потом rename.
func (s *Store) persistLocked() error {
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	payload := fileFormat{
		UpdatedAt: time.Now().UTC(),
		Profiles:  make(map[int64]*Profile, len(s.profiles)),
		OptedIn:   make([]int64, 0, len(s.optIn)),
	}
	for id, p := range s.profiles {
		payload.Profiles[id] = p
	}
	for id := range s.optIn {
		payload.OptedIn = append(payload.OptedIn, id)
	}
	sort.Slice(payload.OptedIn, func(i, j int) bool { return payload.OptedIn[i] < payload.OptedIn[j] })

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

// fileFormat — структура JSON-файла.
type fileFormat struct {
	UpdatedAt time.Time          `json:"updated_at"`
	Profiles  map[int64]*Profile `json:"profiles"`
	OptedIn   []int64            `json:"opted_in"`
}
