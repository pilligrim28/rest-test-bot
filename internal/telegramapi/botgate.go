package telegramapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// DefaultBotGateURL — базовый адрес прокси BotGate (https://bot-gate.ru/docs).
const DefaultBotGateURL = "https://bot-gate.ru/api/v1/bots"

// botGateSignatureHeader — HMAC-SHA256 (hex) тела запроса, подписанный webhook_secret бота.
const botGateSignatureHeader = "X-BotGate-Signature"

// bearerClient добавляет заголовок Authorization: Bearer <ключ> к каждому запросу.
type bearerClient struct {
	key    string
	client *http.Client
}

func (c bearerClient) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.key)
	return c.client.Do(req)
}

// botGateEndpoint строит шаблон адреса для библиотеки: она подставляет
// (токен, метод), а BotGate токен не нужен — «%.0s» выводит его пустым.
func botGateEndpoint(baseURL, botID string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = DefaultBotGateURL
	}
	return base + "/" + strings.ReplaceAll(strings.TrimSpace(botID), "%", "") + "/%.0s%s"
}

// NewBotGate создаёт клиента, который ходит в Telegram через прокси BotGate.
// Токен бота хранится в кабинете BotGate, здесь нужны только ID бота и API-ключ.
func NewBotGate(baseURL, botID, apiKey string) (*API, error) {
	client := bearerClient{key: strings.TrimSpace(apiKey), client: &http.Client{Timeout: 70 * time.Second}}
	bot, err := tgbotapi.NewBotAPIWithClient("botgate", botGateEndpoint(baseURL, botID), client)
	if err != nil {
		return nil, fmt.Errorf("BotGate не ответил на getMe (проверьте BOTGATE_API_KEY и BOTGATE_BOT_ID): %w", err)
	}
	return &API{bot: bot, fileIDs: make(map[string]string)}, nil
}

// BotGateWebhookHandler принимает обновления, которые пересылает BotGate,
// и проверяет подпись X-BotGate-Signature.
func BotGateWebhookHandler(secret string, updates chan<- Update) http.Handler {
	return webhookHandler(func(r *http.Request, body []byte) bool {
		return validBotGateSignature(secret, body, r.Header.Get(botGateSignatureHeader))
	}, updates)
}

func validBotGateSignature(secret string, body []byte, got string) bool {
	if secret == "" || got == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(strings.ToLower(strings.TrimSpace(got))))
}
