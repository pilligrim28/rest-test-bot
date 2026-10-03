// Package config читает настройки бота из переменных окружения.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	envToken    = "BOT_TOKEN"
	envTokenAlt = "TELEGRAM_BOT_TOKEN"
	envAdmins   = "ADMIN_IDS"
	envAdminAlt = "ADMIN_ID"
	envDataFile = "DATA_FILE"
	envDelayMS  = "BROADCAST_DELAY_MS"

	defaultDataFile = "data/users.json"
	defaultDelayMS  = 60
)

// Config — полная конфигурация приложения.
type Config struct {
	// Token — токен бота от @BotFather.
	Token string
	// AdminIDs — Telegram ID тех, кому доступны админ-команды.
	AdminIDs map[int64]struct{}
	// DataFile — путь к JSON-файлу с профилями.
	DataFile string
	// BroadcastDelay — пауза между сообщениями рассылки (rate limit).
	BroadcastDelay time.Duration
}

// Load собирает конфигурацию из окружения и проверяет обязательные поля.
// Файл .env загружается автоматически, поэтому «go run» работает без внешних обёрток.
func Load() (*Config, error) {
	loadDotEnv()

	cfg := &Config{
		Token:    firstNonEmpty(os.Getenv(envToken), os.Getenv(envTokenAlt)),
		AdminIDs: make(map[int64]struct{}),
	}

	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("не задан токен бота: установите %s", envToken)
	}

	admins := firstNonEmpty(os.Getenv(envAdmins), os.Getenv(envAdminAlt))
	for _, raw := range strings.Split(admins, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("некорректный ID администратора %q: %w", raw, err)
		}
		cfg.AdminIDs[id] = struct{}{}
	}

	cfg.DataFile = firstNonEmpty(os.Getenv(envDataFile), defaultDataFile)

	delayMS := defaultDelayMS
	if raw := strings.TrimSpace(os.Getenv(envDelayMS)); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("некорректная задержка рассылки %q: %w", raw, err)
		}
		delayMS = parsed
	}
	// Telegram допускает ~20 сообщений в секунду: не даём случайно ускориться.
	if delayMS < 50 {
		delayMS = 50
	}
	cfg.BroadcastDelay = time.Duration(delayMS) * time.Millisecond

	return cfg, nil
}

// IsAdmin сообщает, есть ли у пользователя доступ к админ-командам.
func (c *Config) IsAdmin(id int64) bool {
	_, ok := c.AdminIDs[id]
	return ok
}

// AdminCount возвращает число настроенных администраторов.
func (c *Config) AdminCount() int { return len(c.AdminIDs) }

// Validate проверяет конфигурацию на типичные ошибки запуска.
func (c *Config) Validate() error {
	if c.Token == "" {
		return errors.New("токен бота пуст")
	}
	if c.DataFile == "" {
		return errors.New("не указан путь к файлу данных")
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// loadDotEnv ищет файл .env рядом с запуском приложения и загружает его.
// Уже установленные (непустые) переменные окружения имеют приоритет над значениями из файла.
func loadDotEnv() {
	for _, path := range dotenvCandidates() {
		if applyDotEnv(path) {
			return
		}
	}
}

// applyDotEnv читает указанный файл и выставляет переменные через os.Setenv.
// Возвращает true, если файл найден и успешно разобран.
func applyDotEnv(path string) bool {
	values, err := godotenv.Read(path)
	if err != nil {
		return false
	}
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" || strings.TrimSpace(os.Getenv(key)) != "" {
			continue
		}
		// os.Setenv используется вместо godotenv.Load намеренно: в некоторых
		// окружениях (например, при t.Setenv в тестах) Load не перезаписывает
		// уже существующие пустые переменные.
		os.Setenv(key, value)
	}
	return true
}

func dotenvCandidates() []string {
	candidates := []string{".env"}

	// Переменная ENV_FILE позволяет указать явный путь к файлу окружения.
	if custom := strings.TrimSpace(os.Getenv("ENV_FILE")); custom != "" {
		candidates = append([]string{custom}, candidates...)
	}

	// Если «go run cmd/bot/main.go» запускается из корня проекта, бинарник лежит во временной папке —
	// добавляем текущую директорию процесса и её родителя на случай запуска из подпапки.
	if exeDir, err := os.Executable(); err == nil {
		candidates = append(candidates,
			filepath.Join(exeDir, ".env"),
			filepath.Join(filepath.Dir(exeDir), ".env"),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, ".env"))
	}
	return candidates
}
