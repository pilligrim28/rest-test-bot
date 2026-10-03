// Package telegramapi — тонкая обёртка над Telegram Bot API.
//
// Обёртка решает три задачи:
//   - единый интерфейс для отправки сообщений и прикреплённых кнопок;
//   - корректная обработка «Conflict: can't use getUpdates method while
//     webhook is active» (409): при старте вебхук принудительно удаляется,
//     а конфликтные ошибки в рантайме чинятся автоматически без падения бота;
//   - long polling с собственным смещением (offset) и ретраями.
package telegramapi

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Update — входящее событие: текстовое сообщение или нажатие inline-кнопки.
type Update struct {
	ID         int
	Text       string
	SenderID   int64
	SenderName string
	Callback   string // непусто, если это нажатие кнопки
	callbackID string // id callback-запроса для ответа «ack»
}

// IsCallback сообщает, что обновление — нажатие inline-кнопки.
func (u Update) IsCallback() bool { return u.Callback != "" }

// API — клиент Bot API.
type API struct {
	bot *tgbotapi.BotAPI
}

// New создаёт клиента и проверяет токен вызовом getMe.
func New(token string) (*API, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("не удалось проверить токен бота (getMe): %w", err)
	}
	return &API{bot: bot}, nil
}

// BotUsername — имя бота (@something_bot), полезно для логов и /help.
func (a *API) BotUsername() string { return a.bot.Self.UserName }

// EnsureNoWebhook удаляет активный вебхук, чтобы long polling не получал 409 Conflict.
func (a *API) EnsureNoWebhook() error {
	_, err := a.bot.Request(tgbotapi.DeleteWebhookConfig{})
	return err
}

// SetWebhook устанавливает вебхук (используется, когда WEBHOOK_URL задан явно).
func (a *API) SetWebhook(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("некорректный WEBHOOK_URL: %w", err)
	}
	_, err = a.bot.Request(tgbotapi.WebhookConfig{URL: u})
	return err
}

// SendOptions — параметры отправляемого сообщения.
type SendOptions struct {
	Markdown        bool
	InlineKeyboard  [][]string // строки inline-кнопок; пары: текст, callback_data
	ReplyKeyboard   []string   // обычные кнопки ответа одним рядом
	OneTimeKeyboard bool
	RemoveKeyboard  bool // скрыть клавиатуру ответа
}

// Send отправляет сообщение пользователю. Пустой chatID возвращает ошибку без запроса.
func (a *API) Send(chatID int64, text string, opts SendOptions) error {
	if chatID == 0 {
		return errors.New("неизвестный получатель")
	}
	msg := tgbotapi.NewMessage(chatID, text)
	if opts.Markdown {
		msg.ParseMode = "Markdown"
		msg.DisableWebPagePreview = true
	}
	switch {
	case len(opts.InlineKeyboard) > 0:
		kb := make([][]tgbotapi.InlineKeyboardButton, 0, len(opts.InlineKeyboard))
		for _, row := range opts.InlineKeyboard {
			buttons := make([]tgbotapi.InlineKeyboardButton, 0, len(row)/2)
			for i := 0; i+1 < len(row); i += 2 {
				buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData(row[i], row[i+1]))
			}
			if len(buttons) > 0 {
				kb = append(kb, buttons)
			}
		}
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(kb...)
	case len(opts.ReplyKeyboard) > 0:
		row := make([]tgbotapi.KeyboardButton, 0, len(opts.ReplyKeyboard))
		for _, label := range opts.ReplyKeyboard {
			row = append(row, tgbotapi.NewKeyboardButton(label))
		}
		kb := tgbotapi.NewReplyKeyboard(row)
		kb.OneTimeKeyboard = opts.OneTimeKeyboard
		msg.ReplyMarkup = kb
	case opts.RemoveKeyboard:
		msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
	}

	_, err := a.bot.Request(msg)
	return err
}

// AnswerCallback подтверждает нажатие кнопки (убирает «часы» у пользователя).
func (a *API) AnswerCallback(u Update, alert string) {
	if u.callbackID == "" {
		return
	}
	cfg := tgbotapi.NewCallback(u.callbackID, "")
	if alert != "" {
		cfg.ShowAlert = true
		cfg.Text = alert
	}
	// Ответ на callback не критичен: игнорируем ошибки доставки ack.
	_, _ = a.bot.Request(cfg)
}

// PollUpdates запускает long polling в отдельной горутине.
// Канал updates закрывается при остановке через канал stop.
func (a *API) PollUpdates(updates chan Update, stop <-chan struct{}) {
	offset := 0
	for {
		select {
		case <-stop:
			close(updates)
			return
		default:
		}

		cfg := tgbotapi.UpdateConfig{Offset: offset, Limit: 100, Timeout: 30}
		list, err := a.bot.GetUpdates(cfg)
		if err != nil {
			if isConflict(err) {
				// Кто-то снова поставил вебхук — чиним и продолжаем без паузы.
				log.Printf("[telegram] вебхук активен, удаляю его автоматически…")
				if derr := a.EnsureNoWebhook(); derr != nil {
					log.Printf("[telegram] deleteWebhook не удался: %v", derr)
					if waitOrStop(stop, 3*time.Second) {
						close(updates)
						return
					}
				}
				continue
			}
			log.Printf("[telegram] getUpdates: %v — повтор через 3 сек", err)
			if waitOrStop(stop, 3*time.Second) {
				close(updates)
				return
			}
			continue
		}

		for _, upd := range list {
			offset = upd.UpdateID + 1

			parsed, ok := toUpdate(upd)
			if !ok {
				continue
			}
			select {
			case updates <- parsed:
			case <-stop:
				close(updates)
				return
			}
		}
	}
}

func toUpdate(upd tgbotapi.Update) (Update, bool) {
	if cb := upd.CallbackQuery; cb != nil && cb.Data != "" {
		var senderID int64
		var senderName string
		if cb.From != nil {
			senderID = cb.From.ID
			senderName = displayName(cb.From)
		}
		return Update{
			ID:         upd.UpdateID,
			SenderID:   senderID,
			SenderName: senderName,
			Callback:   strings.TrimSpace(cb.Data),
			callbackID: cb.ID,
		}, true
	}
	if msg := upd.Message; msg != nil && msg.Text != "" {
		var senderID int64
		var senderName string
		if msg.From != nil {
			senderID = msg.From.ID
			senderName = displayName(msg.From)
		}
		return Update{
			ID:         upd.UpdateID,
			Text:       strings.TrimSpace(msg.Text),
			SenderID:   senderID,
			SenderName: senderName,
		}, true
	}
	return Update{}, false
}

func displayName(u *tgbotapi.User) string {
	name := strings.TrimSpace(u.FirstName)
	if u.UserName != "" {
		if name == "" {
			return "@" + u.UserName
		}
		return name + " (@" + u.UserName + ")"
	}
	return name
}

// isConflict определяет ошибку 409 (вебхук активен) в любом виде.
func isConflict(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *tgbotapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == 409 {
		return true
	}
	return strings.Contains(err.Error(), "Conflict")
}

// waitOrStop спит d или выходит раньше при закрытии stop. Возвращает true, если пора завершаться.
func waitOrStop(stop <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-stop:
		return true
	case <-timer.C:
		return false
	}
}
