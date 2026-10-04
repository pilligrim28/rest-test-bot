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
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Update — входящее событие: текстовое сообщение или нажатие inline-кнопки.
type Update struct {
	ID         int
	Text       string
	SenderID   int64
	SenderName string
	// Username — @логин из профиля Telegram без «@» (пусто, если его нет).
	Username string
	// FirstName — имя из профиля Telegram.
	FirstName string
	Callback  string // непусто, если это нажатие кнопки
	// Contact — заполнен, если пользователь поделился номером кнопкой Telegram.
	Contact    *Contact
	callbackID string // id callback-запроса для ответа «ack»
}

// Contact — номер телефона, переданный кнопкой «Поделиться номером».
type Contact struct {
	Phone string
	// UserID — чей это номер; 0, если контакт не привязан к аккаунту Telegram.
	UserID int64
}

// IsCallback сообщает, что обновление — нажатие inline-кнопки.
func (u Update) IsCallback() bool { return u.Callback != "" }

// API — клиент Bot API.
type API struct {
	bot *tgbotapi.BotAPI

	// fileIDs — кэш «локальный путь → file_id»: файл загружается в Telegram
	// один раз, дальше отправляется по file_id без повторной загрузки.
	mu      sync.Mutex
	fileIDs map[string]string
}

// New создаёт клиента и проверяет токен вызовом getMe.
func New(token string) (*API, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("не удалось проверить токен бота (getMe): %w", err)
	}
	return &API{bot: bot, fileIDs: make(map[string]string)}, nil
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
	// RequestContact — подпись кнопки «поделиться номером» (обычная клавиатура).
	// Остальные кнопки ReplyKeyboard добавляются вторым рядом.
	RequestContact string

	// Photo — картинка к сообщению: путь к файлу, https-ссылка или file_id.
	Photo string
	// Audio — аудиофайл: путь к файлу, https-ссылка или file_id.
	Audio string
}

// captionLimit — максимальная длина подписи к медиа в Telegram.
const captionLimit = 1024

// Send отправляет сообщение пользователю. Если задана картинка или аудио,
// текст становится подписью (или идёт отдельным сообщением, если длиннее
// 1024 символов). Если медиа отправить не удалось, уходит обычный текст —
// пользователь не останется без вопроса.
func (a *API) Send(chatID int64, text string, opts SendOptions) error {
	if chatID == 0 {
		return errors.New("неизвестный получатель")
	}
	markup := buildMarkup(opts)
	parseMode := ""
	if opts.Markdown {
		parseMode = "Markdown"
	}

	media := opts.Photo
	if opts.Audio != "" {
		media = opts.Audio
	}
	if media != "" {
		withCaption := text != "" && utf8.RuneCountInString(text) <= captionLimit
		caption := ""
		if withCaption {
			caption = text
		}
		var mediaMarkup interface{}
		if withCaption || text == "" {
			mediaMarkup = markup
		}
		err := a.sendMedia(chatID, media, opts.Audio != "", caption, parseMode, mediaMarkup)
		if err != nil {
			log.Printf("[telegram] не удалось отправить медиа %q: %v — отправляю текстом", media, err)
		} else if withCaption || text == "" {
			return nil
		}
	}

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = parseMode
	msg.DisableWebPagePreview = opts.Markdown
	if markup != nil {
		msg.ReplyMarkup = markup
	}
	_, err := a.bot.Send(msg)
	return err
}

func buildMarkup(opts SendOptions) interface{} {
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
		return tgbotapi.NewInlineKeyboardMarkup(kb...)
	case opts.RequestContact != "" || len(opts.ReplyKeyboard) > 0:
		var rows [][]tgbotapi.KeyboardButton
		if opts.RequestContact != "" {
			rows = append(rows, tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButtonContact(opts.RequestContact)))
		}
		if len(opts.ReplyKeyboard) > 0 {
			row := make([]tgbotapi.KeyboardButton, 0, len(opts.ReplyKeyboard))
			for _, label := range opts.ReplyKeyboard {
				row = append(row, tgbotapi.NewKeyboardButton(label))
			}
			rows = append(rows, row)
		}
		kb := tgbotapi.NewReplyKeyboard(rows...)
		kb.OneTimeKeyboard = opts.OneTimeKeyboard
		kb.ResizeKeyboard = true
		return kb
	case opts.RemoveKeyboard:
		return tgbotapi.NewRemoveKeyboard(true)
	}
	return nil
}

// sendMedia отправляет фото или аудио. Локальный файл загружается один раз,
// затем используется сохранённый file_id.
func (a *API) sendMedia(chatID int64, source string, audio bool, caption, parseMode string, markup interface{}) error {
	file, localPath := a.resolveFile(source)

	var cfg tgbotapi.Chattable
	if audio {
		c := tgbotapi.NewAudio(chatID, file)
		c.Caption, c.ParseMode = caption, parseMode
		if markup != nil {
			c.ReplyMarkup = markup
		}
		cfg = c
	} else {
		c := tgbotapi.NewPhoto(chatID, file)
		c.Caption, c.ParseMode = caption, parseMode
		if markup != nil {
			c.ReplyMarkup = markup
		}
		cfg = c
	}

	sent, err := a.bot.Send(cfg)
	if err != nil {
		if localPath != "" {
			// Возможно, устарел file_id — в следующий раз загрузим файл заново.
			a.mu.Lock()
			delete(a.fileIDs, localPath)
			a.mu.Unlock()
		}
		return err
	}
	if localPath != "" {
		if id := uploadedFileID(sent, audio); id != "" {
			a.mu.Lock()
			a.fileIDs[localPath] = id
			a.mu.Unlock()
		}
	}
	return nil
}

// resolveFile превращает строку из конфигурации в объект файла Telegram.
// Второе значение — локальный путь, если файл нужно закэшировать.
func (a *API) resolveFile(source string) (tgbotapi.RequestFileData, string) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return tgbotapi.FileURL(source), ""
	}
	if st, err := os.Stat(source); err == nil && !st.IsDir() {
		a.mu.Lock()
		id, ok := a.fileIDs[source]
		a.mu.Unlock()
		if ok {
			return tgbotapi.FileID(id), source
		}
		return tgbotapi.FilePath(source), source
	}
	return tgbotapi.FileID(source), ""
}

func uploadedFileID(m tgbotapi.Message, audio bool) string {
	if audio {
		if m.Audio != nil {
			return m.Audio.FileID
		}
		return ""
	}
	if n := len(m.Photo); n > 0 {
		return m.Photo[n-1].FileID
	}
	return ""
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
		u := Update{
			ID:         upd.UpdateID,
			SenderID:   senderID,
			SenderName: senderName,
			Callback:   strings.TrimSpace(cb.Data),
			callbackID: cb.ID,
		}
		fillProfile(&u, cb.From)
		return u, true
	}
	if msg := upd.Message; msg != nil && (msg.Text != "" || msg.Contact != nil) {
		// Только личные чаты: в группах бот данные не собирает.
		if msg.Chat != nil && !msg.Chat.IsPrivate() {
			return Update{}, false
		}
		var senderID int64
		var senderName string
		if msg.From != nil {
			senderID = msg.From.ID
			senderName = displayName(msg.From)
		}
		u := Update{
			ID:         upd.UpdateID,
			Text:       strings.TrimSpace(msg.Text),
			SenderID:   senderID,
			SenderName: senderName,
		}
		if c := msg.Contact; c != nil {
			u.Contact = &Contact{Phone: c.PhoneNumber, UserID: c.UserID}
		}
		fillProfile(&u, msg.From)
		return u, true
	}
	return Update{}, false
}

func fillProfile(u *Update, from *tgbotapi.User) {
	if from == nil {
		return
	}
	u.Username = from.UserName
	u.FirstName = strings.TrimSpace(from.FirstName)
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
