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
	// envWebhook — URL вебхука. Если переменная НЕ задана, бот при старте
	// принудительно удаляет вебхук (deleteWebhook) и работает через long polling
	// (getUpdates). Иначе Telegram отвечает «Conflict: can't use getUpdates
	// method while webhook is active».
	envWebhook = "WEBHOOK_URL"
	// envPracticeURL — ссылка на бесплатную аудиопрактику «Переключатель».
	// Если не задана, используется значение по умолчанию из internal/quiz.
	envPracticeURL = "PRACTICE_URL"

	defaultDataFile = "data/users.json"
	defaultDelayMS  = 60

	defaultRetentionDays = 365
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
	// WebhookURL — пустая строка означает «работать через long polling и
	// удалить активный вебхук при старте».
	WebhookURL string
	// WebhookListen — адрес HTTP-сервера вебхука (по умолчанию :8080).
	WebhookListen string
	// WebhookPath — путь, на который Telegram шлёт обновления.
	WebhookPath string
	// WebhookSecret — секрет для заголовка X-Telegram-Bot-Api-Secret-Token.
	WebhookSecret string
	// PracticeURL — ссылка на аудиопрактику; пусто — значение по умолчанию из internal/quiz.
	PracticeURL string
	// PracticeAudio — аудиофайл практики (путь, URL или file_id). Если задан,
	// практика приходит прямо в чат и контакт для доставки не нужен.
	PracticeAudio string

	// WelcomePhoto — картинка к приветствию (путь, URL или file_id).
	WelcomePhoto string
	// QuestionPhotos — картинки к вопросам: индекс 0 — вопрос 1. Пустая строка — без картинки.
	QuestionPhotos []string
	// ResultPhotos — картинки к результату: индекс 0 — преобладает ответ 1,
	// 1 — ответ 2, 2 — ответ 3. ResultTiePhoto — при равенстве баллов.
	ResultPhotos   []string
	ResultTiePhoto string

	// OperatorName, OperatorCity, OperatorContact, PolicyURL — реквизиты для текста согласия.
	OperatorName    string
	OperatorCity    string
	OperatorContact string
	PolicyURL       string
	// CollectPhone — предлагать ли поделиться номером телефона.
	CollectPhone bool
	// Retention — срок хранения профиля с последнего обновления; 0 — бессрочно.
	Retention time.Duration
}

// QuestionCount — число вопросов, для которых ищутся картинки.
const QuestionCount = 5

// ResultPhoto возвращает картинку результата для варианта ответа n (1–3) или пустую строку.
func (c *Config) ResultPhoto(n int) string {
	if c == nil || n < 1 || n > len(c.ResultPhotos) {
		return ""
	}
	return c.ResultPhotos[n-1]
}

// QuestionPhoto возвращает картинку для вопроса n (1-based) или пустую строку.
func (c *Config) QuestionPhoto(n int) string {
	if c == nil || n < 1 || n > len(c.QuestionPhotos) {
		return ""
	}
	return c.QuestionPhotos[n-1]
}

// Load собирает конфигурацию из окружения и проверяет обязательные поля.
// Файл .env загружается автоматически, поэтому «go run» работает без внешних обёрток.
func Load() (*Config, error) {
	loadDotEnv()

	cfg := &Config{
		Token:       firstNonEmpty(os.Getenv(envToken), os.Getenv(envTokenAlt)),
		AdminIDs:    make(map[int64]struct{}),
		WebhookURL:  firstNonEmpty(os.Getenv(envWebhook)),
		PracticeURL: firstNonEmpty(os.Getenv(envPracticeURL)),
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

	cfg.WebhookListen = firstNonEmpty(os.Getenv("WEBHOOK_LISTEN"), ":8080")
	cfg.WebhookPath = firstNonEmpty(os.Getenv("WEBHOOK_PATH"), "/telegram/webhook")
	cfg.WebhookSecret = firstNonEmpty(os.Getenv("WEBHOOK_SECRET"))
	if cfg.WebhookURL != "" && cfg.WebhookSecret == "" {
		return nil, errors.New("для режима вебхука задайте WEBHOOK_SECRET (латиница, цифры, _ и -, до 256 символов)")
	}

	cfg.PracticeAudio = firstNonEmpty(os.Getenv("PRACTICE_AUDIO"))
	cfg.WelcomePhoto = firstNonEmpty(os.Getenv("WELCOME_PHOTO"), os.Getenv("WELCOME_PHOTO_URL"), existingFile("assets/welcome.jpg", "assets/welcome.png"))
	common := firstNonEmpty(os.Getenv("QUESTION_PHOTO"))
	cfg.QuestionPhotos = make([]string, QuestionCount)
	for i := 1; i <= QuestionCount; i++ {
		cfg.QuestionPhotos[i-1] = firstNonEmpty(
			os.Getenv(fmt.Sprintf("QUESTION_PHOTO_%d", i)),
			common,
			existingFile(fmt.Sprintf("assets/question_%d.jpg", i), fmt.Sprintf("assets/question_%d.png", i)),
		)
	}

	cfg.ResultPhotos = make([]string, 3)
	for i := 1; i <= 3; i++ {
		cfg.ResultPhotos[i-1] = firstNonEmpty(
			os.Getenv(fmt.Sprintf("RESULT_PHOTO_%d", i)),
			existingFile(fmt.Sprintf("assets/result_%d.jpg", i), fmt.Sprintf("assets/result_%d.png", i)),
		)
	}
	cfg.ResultTiePhoto = firstNonEmpty(os.Getenv("RESULT_PHOTO_TIE"), existingFile("assets/result_tie.jpg", "assets/result_tie.png"))

	cfg.OperatorName = firstNonEmpty(os.Getenv("OPERATOR_NAME"))
	cfg.OperatorCity = firstNonEmpty(os.Getenv("OPERATOR_CITY"))
	cfg.OperatorContact = firstNonEmpty(os.Getenv("OPERATOR_CONTACT"))
	cfg.PolicyURL = firstNonEmpty(os.Getenv("POLICY_URL"))
	cfg.CollectPhone = parseBool(os.Getenv("COLLECT_PHONE"))

	retentionDays := defaultRetentionDays
	if raw := strings.TrimSpace(os.Getenv("DATA_RETENTION_DAYS")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return nil, fmt.Errorf("некорректный DATA_RETENTION_DAYS %q", raw)
		}
		retentionDays = parsed
	}
	cfg.Retention = time.Duration(retentionDays) * 24 * time.Hour

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

// existingFile возвращает первый существующий файл из списка или пустую строку.
func existingFile(paths ...string) string {
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func parseBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on", "да":
		return true
	}
	return false
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
