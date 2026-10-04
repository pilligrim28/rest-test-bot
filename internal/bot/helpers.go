package bot

// Вспомогательные функции и тексты справки.

import (
	"log"
	"strings"

	"github.com/restquiz/rest-test-bot/internal/telegramapi"
)

func (b *Bot) send(send Sender, id int64, text string, opts telegramapi.SendOptions) {
	if err := send(id, text, opts); err != nil {
		log.Printf("[bot] отправка %d: %v", id, err)
	}
}

func (b *Bot) reply(send Sender, id int64, text string) {
	b.send(send, id, text, telegramapi.SendOptions{})
}

// parseAnswer разбирает текстовый ответ: 1/2/3, А/Б/В, «пропустить».
func parseAnswer(text string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "пропустить", "skip", "ничего из этого", "-":
		return -1, true
	case "1", "а", "a":
		return 0, true
	case "2", "б", "b":
		return 1, true
	case "3", "в", "v":
		return 2, true
	}
	return 0, false
}

// splitChunks режет текст на куски не длиннее max рун по границам абзацев.
func splitChunks(text string, max int) []string {
	var out []string
	cur := ""
	for _, para := range strings.Split(text, "\n\n") {
		switch {
		case cur == "":
			cur = para
		case len([]rune(cur))+2+len([]rune(para)) <= max:
			cur += "\n\n" + para
		default:
			out = append(out, cur)
			cur = para
		}
		for len([]rune(cur)) > max {
			r := []rune(cur)
			out = append(out, string(r[:max]))
			cur = string(r[max:])
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func helpText(isAdmin bool) string {
	text := "Я провожу короткий тест про вечерний отдых.\n\n" +
		"/start — приветствие и запуск теста\n/test — пройти тест заново\n" +
		"/mydata — что я о вас храню\n/forget — отозвать согласие и удалить данные\n/privacy — как я обращаюсь с данными\n\n" +
		"Отвечать можно кнопками или цифрами 1–3 (или А/Б/В)."
	if isAdmin {
		text += "\n\nАдмин-команды: /admin"
	}
	return text
}

const adminHelpText = "Админ-команды:\n/admin — эта справка\n/stats — статистика\n/users — список подписчиков\n" +
	"/broadcast — рассылка подписчикам\n/send <ID> <текст> — личное сообщение\n/cancel — отменить рассылку"
