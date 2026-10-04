package bot

// Админ-команды: статистика, список, рассылка.

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pilligrim28/rest-test-bot/internal/telegramapi"
)

func (b *Bot) adminStats(send Sender, id int64) {
	profiles := b.store.List()
	counts := map[string]int{}
	phones := 0
	for _, p := range profiles {
		key := p.Headline
		if key == "" {
			key = "(без результата)"
		}
		counts[key]++
		if p.Phone != "" {
			phones++
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Завершено тестов (всего): %d\nПодписчиков с согласием: %d\nИз них оставили телефон: %d\n\nРезультаты подписчиков:\n",
		b.store.TestsCompleted(), len(profiles), phones)
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&sb, "• %s — %d\n", k, counts[k])
	}
	b.reply(send, id, sb.String())
}

func (b *Bot) adminUsers(send Sender, id int64) {
	profiles := b.store.List()
	if len(profiles) == 0 {
		b.reply(send, id, "Пока никто не подписался.")
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Подписчиков: %d\n\n", len(profiles))
	for i, p := range profiles {
		fmt.Fprintf(&sb, "%d. %s %s, тел. %s — %s (ID %d)\n", i+1, orDash(p.FirstName), orDash(p.Username), orDash(p.Phone), orDash(p.Headline), p.TelegramID)
	}
	for _, chunk := range splitChunks(sb.String(), 4000) {
		b.reply(send, id, chunk)
	}
}

func (b *Bot) adminSendDirect(adminID int64, raw string, send Sender) {
	fields := strings.Fields(raw)
	if len(fields) < 3 {
		b.reply(send, adminID, "Формат: /send <Telegram ID> <текст сообщения>")
		return
	}
	target, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		b.reply(send, adminID, "ID должен быть числом. Пример: /send 123456789 Привет!")
		return
	}
	// Текст после ID с сохранением переносов строк.
	idx := strings.Index(raw, fields[1]) + len(fields[1])
	msg := strings.TrimSpace(raw[idx:])
	if err := send(target, msg, telegramapi.SendOptions{}); err != nil {
		b.reply(send, adminID, fmt.Sprintf("Не отправилось пользователю %d: %v", target, err))
		return
	}
	b.reply(send, adminID, fmt.Sprintf("Отправлено пользователю %d.", target))
}

// Рассылка идёт только тем, кто дал оба согласия (ст. 18 ФЗ «О рекламе»,
// ст. 9 152-ФЗ). К каждому сообщению добавляется способ отписаться.
func (b *Bot) startBroadcast(id int64, send Sender) {
	n := len(b.store.Subscribers())
	if n == 0 {
		b.reply(send, id, "Подписчиков пока нет — рассылать некому.")
		return
	}
	b.mu.Lock()
	b.broadcast[id] = &broadcastDraft{step: "text"}
	b.mu.Unlock()
	b.reply(send, id, fmt.Sprintf("Рассылка подписчикам (%d). Пришлите текст сообщения одним сообщением. Отмена — /cancel.", n))
}

func (b *Bot) handleBroadcastCallback(id int64, action string, draft *broadcastDraft, send Sender) {
	switch action {
	case bcCancel:
		b.mu.Lock()
		delete(b.broadcast, id)
		b.mu.Unlock()
		b.reply(send, id, "Рассылка отменена.")
	case bcSend:
		if draft == nil || draft.step != "confirm" || !b.cfg.IsAdmin(id) {
			b.reply(send, id, "Мастер рассылки уже завершён. Начните заново: /broadcast")
			return
		}
		b.runBroadcast(id, draft, send)
	default:
		b.reply(send, id, "Эта кнопка устарела. Начните заново: /broadcast")
	}
}

func (b *Bot) continueBroadcast(id int64, draft *broadcastDraft, text string, send Sender) {
	draft.text = strings.TrimSpace(text)
	if draft.text == "" {
		b.reply(send, id, "Получилось пусто — пришлите текст ещё раз или /cancel.")
		return
	}
	draft.step = "confirm"
	n := len(b.store.Subscribers())
	b.send(send, id, "Предпросмотр:\n\n"+truncate(draft.text, 500)+unsubscribeFooter+"\n\nПолучателей: "+strconv.Itoa(n),
		telegramapi.SendOptions{InlineKeyboard: [][]string{
			{"✅ Отправить " + strconv.Itoa(n) + " получателям", "bc:" + bcSend},
			{"Отмена", "bc:" + bcCancel},
		}})
}

func (b *Bot) runBroadcast(adminID int64, draft *broadcastDraft, send Sender) {
	b.mu.Lock()
	delete(b.broadcast, adminID)
	b.mu.Unlock()

	recipients := b.store.Subscribers()
	text := draft.text + unsubscribeFooter
	go func() {
		var sent, failed int
		for _, rid := range recipients {
			if err := send(rid, text, telegramapi.SendOptions{}); err != nil {
				failed++
				log.Printf("[broadcast] получателю %d: %v", rid, err)
			} else {
				sent++
			}
			time.Sleep(b.cfg.BroadcastDelay)
		}
		b.reply(send, adminID, fmt.Sprintf("Рассылка завершена: доставлено %d, ошибок %d (из %d).", sent, failed, len(recipients)))
	}()
	b.reply(send, adminID, fmt.Sprintf("Отправляю %d сообщений — займёт примерно %s. Результат пришлю сюда.",
		len(recipients), (time.Duration(len(recipients))*b.cfg.BroadcastDelay).Round(time.Second)))
}
