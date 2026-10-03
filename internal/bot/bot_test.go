package bot

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restquiz/rest-test-bot/internal/config"
	"github.com/restquiz/rest-test-bot/internal/store"
	"github.com/restquiz/rest-test-bot/internal/telegramapi"
)

// fakeSender собирает все исходящие сообщения.
type fakeSender struct {
	mu   sync.Mutex
	sent []msg
}

type msg struct {
	chatID int64
	text   string
	opts   telegramapi.SendOptions
}

func (f *fakeSender) send(chatID int64, text string, opts telegramapi.SendOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg{chatID, text, opts})
	return nil
}

func (f *fakeSender) last() msg {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return msg{}
	}
	return f.sent[len(f.sent)-1]
}

func (f *fakeSender) countTo(chatID int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.sent {
		if m.chatID == chatID {
			n++
		}
	}
	return n
}

func newTestBot(t *testing.T) (*Bot, *store.Store, *fakeSender) {
	t.Helper()
	cfg := &config.Config{
		Token:      "test",
		AdminIDs:   map[int64]struct{}{111: {}},
		DataFile:   t.TempDir() + "/users.json",
		WebhookURL: "",
	}
	st, err := store.New(cfg.DataFile)
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeSender{}
	b := New(cfg, st, nil)
	return b, st, fs
}

func text(id int64, s string) telegramapi.Update {
	return telegramapi.Update{SenderID: id, SenderName: "Тест (@test)", Text: s}
}

func cb(id int64, data string) telegramapi.Update {
	return telegramapi.Update{SenderID: id, SenderName: "Тест (@test)", Callback: data}
}

// runQuiz отвечает на все 5 вопросов через inline-кнопки с указанными вариантами.
func runQuiz(t *testing.T, b *Bot, fs *fakeSender, id int64, choices []int) {
	t.Helper()
	b.HandleUpdate(text(id, "/start"), fs.send)
	b.HandleUpdate(cb(id, "start_test"), fs.send) // вопрос 1
	for i, choice := range choices {
		last := fs.last()
		// находим callback кнопки выбранного варианта в текущем вопросе
		var data string
		for _, row := range last.opts.InlineKeyboard {
			if strings.HasPrefix(row[1], "q:") && strings.HasSuffix(row[1], ":"+string(rune('0'+choice))) {
				data = row[1]
			}
		}
		if data == "" {
			t.Fatalf("не найдена кнопка варианта %d в сообщении: %+v", choice, last.opts.InlineKeyboard)
		}
		_ = i
		b.HandleUpdate(cb(id, data), fs.send)
	}
}

func TestFullQuizConsentFlow(t *testing.T) {
	b, st, fs := newTestBot(t)
	const id int64 = 42

	runQuiz(t, b, fs, id, []int{0, 0, 0, 1, 2}) // категория A побеждает (3 балла)

	// Последнее сообщение — запрос согласия с двумя кнопками.
	last := fs.last()
	if !strings.Contains(last.text, "Согласен") {
		t.Fatalf("ожидал запрос согласия, получил: %q", last.text)
	}

	// Согласие → ожидание логина.
	b.HandleUpdate(cb(id, "agree"), fs.send)
	if !strings.Contains(fs.last().text, "Подтвердите") {
		t.Fatalf("ожидал запрос логина, получил: %q", fs.last().text)
	}

	// Ввод логина текстом → профиль сохранён.
	b.HandleUpdate(text(id, "natulusik"), fs.send)
	p, ok := st.Get(id)
	if !ok {
		t.Fatal("профиль не сохранён после подтверждения логина")
	}
	if p.Login != "@natulusik" {
		t.Errorf("логин = %q, хотим @natulusik", p.Login)
	}
	if p.Headline == "" {
		t.Error("headline пуст")
	}
	if p.Runs != 1 {
		t.Errorf("runs = %d, хотим 1", p.Runs)
	}

	// /mystats показывает результат.
	before := fs.countTo(id)
	b.HandleUpdate(text(id, "/mystats"), fs.send)
	got := fs.last()
	if got.chatID != id || !strings.Contains(got.text, "Прохождений: 1") {
		t.Errorf("/mystats ответил некорректно: %q", got.text)
	}
	if fs.countTo(id) != before+1 {
		t.Error("ожидала ровно один ответ на /mystats")
	}

	// /forget удаляет данные.
	b.HandleUpdate(text(id, "/forget"), fs.send)
	if _, ok := st.Get(id); ok {
		t.Error("профиль должен быть удалён после /forget")
	}
}

func TestDeclineKeepsNoProfile(t *testing.T) {
	b, st, fs := newTestBot(t)
	const id int64 = 7

	runQuiz(t, b, fs, id, []int{1, 1, 1, 1, 1})

	b.HandleUpdate(cb(id, "decline"), fs.send)
	if _, ok := st.Get(id); ok {
		t.Error("после отказа профиль создаваться не должен")
	}
	if n := len(st.Recipients()); n != 1 {
		t.Errorf("отметка об участии должна остаться, recipients = %d", n)
	}
}

func TestInsufficientAnswers(t *testing.T) {
	b, _, fs := newTestBot(t)
	const id int64 = 9

	b.HandleUpdate(text(id, "/test"), fs.send)
	// Пропускаем 3 вопроса, отвечаем на 2 — недостаточно.
	b.HandleUpdate(cb(id, "skip"), fs.send)
	b.HandleUpdate(cb(id, "skip"), fs.send)
	b.HandleUpdate(cb(id, "skip"), fs.send)
	b.HandleUpdate(cb(id, "q:4:0"), fs.send)
	b.HandleUpdate(cb(id, "q:5:0"), fs.send)

	if !strings.Contains(fs.last().text, "недостаточно") {
		t.Errorf("ожидал текст о недостаточности, получил %q", fs.last().text)
	}
}

func TestTextAnswersSupported(t *testing.T) {
	b, st, fs := newTestBot(t)
	const id int64 = 5

	b.HandleUpdate(text(id, "/test"), fs.send)
	b.HandleUpdate(text(id, "1"), fs.send)
	b.HandleUpdate(text(id, "б"), fs.send)
	b.HandleUpdate(text(id, "B"), fs.send)
	b.HandleUpdate(text(id, "Пропустить"), fs.send)
	b.HandleUpdate(text(id, "3"), fs.send)

	// Результат + согласие: последние два сообщения.
	texts := fs.sent[len(fs.sent)-2:]
	if !strings.Contains(texts[0].text, "Ваш результат") {
		t.Errorf("ожидал результат теста, получил %q", texts[0].text)
	}
	if !strings.Contains(texts[1].text, "Согласен") {
		t.Errorf("ожидал запрос согласия, получил %q", texts[1].text)
	}
	_ = st
}

func TestTieShowsBothCategories(t *testing.T) {
	b, _, fs := newTestBot(t)
	const id int64 = 8

	// 2 балла A, 2 балла B, 1 пропуск → равенство двух категорий.
	b.HandleUpdate(text(id, "/test"), fs.send)
	b.HandleUpdate(cb(id, "q:1:0"), fs.send)
	b.HandleUpdate(cb(id, "q:2:0"), fs.send)
	b.HandleUpdate(cb(id, "q:3:1"), fs.send)
	b.HandleUpdate(cb(id, "q:4:1"), fs.send)
	b.HandleUpdate(cb(id, "skip"), fs.send)

	resultMsg := fs.sent[len(fs.sent)-2].text
	if !strings.Contains(resultMsg, "две привычки") {
		t.Errorf("ожидал текст про две привычки, получил %q", resultMsg)
	}
}

func TestAdminCommandsAndBroadcast(t *testing.T) {
	b, st, fs := newTestBot(t)
	const admin int64 = 111
	const user int64 = 222

	// Не-админ не получает админ-команды.
	b.HandleUpdate(text(user, "/stats"), fs.send)
	if strings.Contains(fs.last().text, "Профилей с согласием") {
		t.Error("/stats не должен работать для не-админа")
	}

	// Админ: справка, статистика, список.
	b.HandleUpdate(text(admin, "/admin"), fs.send)
	if !strings.Contains(fs.last().text, "Админ-команды") {
		t.Errorf("ожидал админ-справку, получил %q", fs.last().text)
	}
	b.HandleUpdate(text(admin, "/stats"), fs.send)
	if !strings.Contains(fs.last().text, "Профилей с согласием: 0") {
		t.Errorf("ожидал нулевую статистику, получил %q", fs.last().text)
	}

	// Создадим профиль вручную и запустим рассылку.
	if err := st.Save(store.Profile{TelegramID: user, Login: "@user", Username: "@user", Headline: "X"}); err != nil {
		t.Fatal(err)
	}

	b.HandleUpdate(text(admin, "/broadcast"), fs.send)
	b.HandleUpdate(cb(admin, "bc:bc_all_consent"), fs.send)
	b.HandleUpdate(text(admin, "Привет, это рассылка!"), fs.send)

	if !strings.Contains(fs.last().text, "Предпросмотр") {
		t.Fatalf("ожидал предпросмотр, получил %q", fs.last().text)
	}

	// Подтверждение отправки — процесс асинхронный, ждём финальный отчёт.
	b.HandleUpdate(cb(admin, "bc:bc_send"), fs.send)
	waitFor(t, func() bool {
		last := fs.last()
		return last.chatID == admin && strings.Contains(last.text, "Рассылка завершена")
	})
	if !strings.Contains(fs.last().text, "доставлено 1") {
		t.Errorf("ожидал «доставлено 1», получил %q", fs.last().text)
	}
}

func TestSendDirect(t *testing.T) {
	b, _, fs := newTestBot(t)
	const admin int64 = 111

	b.HandleUpdate(text(admin, "/send 555 Привет!"), fs.send)
	found := false
	for _, m := range fs.sent {
		if m.chatID == 555 && m.text == "Привет!" {
			found = true
		}
	}
	if !found {
		t.Error("личное сообщение не ушло адресату")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("условие не выполнилось за 2 секунды")
}
