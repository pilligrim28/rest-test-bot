// Package config читает настройки бота из переменных окружения.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	
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
func Load() (*Config, error) {
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
