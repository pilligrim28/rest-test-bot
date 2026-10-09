package store

import (
	"sort"
	"time"
)

// Followup — отложенное сообщение после выдачи практики: вопрос «Получилось
// прослушать?», напоминания и предложение подписаться.
//
// Минимизация данных: хранится только ID чата, этап и результат теста — то,
// без чего нельзя продолжить начатый пользователем сценарий. Запись удаляется,
// как только сценарий завершён, и в любом случае — через FollowupTTL.
type Followup struct {
	ChatID int64 `json:"chat_id"`
	// Stage — этап сценария (константы задаются в пакете bot).
	Stage string `json:"stage"`
	// DueAt — когда отправить следующее сообщение; нулевое — ничего не ждём,
	// только ответа пользователя.
	DueAt time.Time `json:"due_at,omitempty"`
	// NoAt — когда пользователь ответил «Нет»: от него считаются напоминания.
	NoAt      time.Time      `json:"no_at,omitempty"`
	Headline  string         `json:"headline,omitempty"`
	Scores    map[string]int `json:"scores,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// FollowupTTL — сколько максимум живёт запись сценария после практики.
const FollowupTTL = 7 * 24 * time.Hour

// SetFollowup создаёт или заменяет запись сценария для чата.
func (s *Store) SetFollowup(f Followup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now().UTC()
	}
	s.followups[f.ChatID] = &f
	return s.persistLocked()
}

// GetFollowup возвращает копию записи сценария.
func (s *Store) GetFollowup(id int64) (Followup, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.followups[id]
	if !ok {
		return Followup{}, false
	}
	return *f, true
}

// DeleteFollowup удаляет запись сценария.
func (s *Store) DeleteFollowup(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.followups[id]; !ok {
		return nil
	}
	delete(s.followups, id)
	return s.persistLocked()
}

// DueFollowups возвращает записи, у которых подошло время, и заодно удаляет
// устаревшие (старше FollowupTTL).
func (s *Store) DueFollowups(now time.Time) []Followup {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []Followup
	changed := false
	for id, f := range s.followups {
		if now.Sub(f.CreatedAt) > FollowupTTL {
			delete(s.followups, id)
			changed = true
			continue
		}
		if !f.DueAt.IsZero() && !now.Before(f.DueAt) {
			due = append(due, *f)
		}
	}
	if changed {
		_ = s.persistLocked()
	}
	sort.Slice(due, func(i, j int) bool { return due[i].DueAt.Before(due[j].DueAt) })
	return due
}
