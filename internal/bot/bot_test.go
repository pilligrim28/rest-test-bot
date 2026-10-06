package bot

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pilligrim28/rest-test-bot/internal/config"
	"github.com/pilligrim28/rest-test-bot/internal/store"
	"github.com/pilligrim28/rest-test-bot/internal/telegramapi"
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
	return telegramapi.Update{SenderID: id, SenderName: "Тест (@test)", Username: "test", FirstName: "Тест", Text: s}
}

func cb(id int64, data string) telegramapi.Update {
	return telegramapi.Update{SenderID: id, SenderName: "Тест (@test)", Username: "test", FirstName: "Тест", Callback: data}
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
			for j := 1; j < len(row); j += 2 {
				if strings.HasPrefix(row[j], "q:") && strings.HasSuffix(row[j], ":"+string(rune('0'+choice))) {
					data = row[j]
				}
			}
		}
		if data == "" {
			t.Fatalf("не найдена кнопка варианта %d в сообщении: %+v", choice, last.opts.InlineKeyboard)
		}
		_ = i
		b.HandleUpdate(cb(id, data), fs.send)
	}
}

// subscribe проходит сценарий «практика → подписка → согласие».
func subscribe(b *Bot, fs *fakeSender, id int64) {
	b.HandleUpdate(cb(id, "practice"), fs.send)
	b.HandleUpdate(cb(id, "sub_yes"), fs.send)
	b.HandleUpdate(cb(id, "pd_yes"), fs.send)
}

func TestFullQuizConsentFlow(t *testing.T) {
	b, st, fs := newTestBot(t)
	const id int64 = 42

	runQuiz(t, b, fs, id, []int{0, 0, 0, 1, 2}) // категория A побеждает (3 балла)

	// После результата никаких данных не запрашиваем — только кнопка практики.
	last := fs.last()
	if !strings.Contains(last.text, "Ваш результат") || last.opts.InlineKeyboard[0][1] != "practice" {
		t.Fatalf("ожидал результат с кнопкой практики, получил: %q", last.text)
	}
	if st.TestsCompleted() != 1 {
		t.Errorf("счётчик тестов = %d, хотим 1", st.TestsCompleted())
	}

	// Практика выдаётся без согласия, затем — предложение подписки.
	b.HandleUpdate(cb(id, "practice"), fs.send)
	if !strings.Contains(fs.sent[len(fs.sent)-2].text, "Переключатель") {
		t.Fatalf("ожидал практику, получил %q", fs.sent[len(fs.sent)-2].text)
	}
	if !strings.Contains(fs.last().text, "Присылать?") {
		t.Fatalf("ожидал предложение подписки, получил %q", fs.last().text)
	}
	if st.Count() != 0 {
		t.Fatal("до согласия ничего сохраняться не должно")
	}

	b.HandleUpdate(cb(id, "sub_yes"), fs.send)
	if !strings.Contains(fs.last().text, "Согласие на обработку персональных данных") {
		t.Fatalf("ожидал отдельное согласие на ПДн, получил %q", fs.last().text)
	}
	if st.Count() != 0 {
		t.Fatal("одного согласия на рассылку недостаточно для сохранения")
	}

	b.HandleUpdate(cb(id, "pd_yes"), fs.send)
	p, ok := st.Get(id)
	if !ok {
		t.Fatal("профиль не сохранён после согласия")
	}
	if p.Username != "@test" || p.FirstName != "Тест" {
		t.Errorf("логин/имя = %q/%q", p.Username, p.FirstName)
	}
	if p.ConsentVersion == "" || p.ConsentMethod == "" || !p.Subscribed() {
		t.Errorf("не сохранены доказательства согласия: %+v", p)
	}
	if p.Headline == "" {
		t.Error("headline пуст")
	}

	b.HandleUpdate(text(id, "/mydata"), fs.send)
	if !strings.Contains(fs.last().text, "Telegram ID: 42") {
		t.Errorf("/mydata ответил некорректно: %q", fs.last().text)
	}

	b.HandleUpdate(text(id, "/forget"), fs.send)
	if _, ok := st.Get(id); ok {
		t.Error("профиль должен быть удалён после /forget")
	}
}

func TestDeclineKeepsNoProfile(t *testing.T) {
	for _, decline := range []string{"sub_no", "pd_no"} {
		b, st, fs := newTestBot(t)
		const id int64 = 7
		runQuiz(t, b, fs, id, []int{1, 1, 1, 1, 1})
		b.HandleUpdate(cb(id, "practice"), fs.send)
		if decline == "pd_no" {
			b.HandleUpdate(cb(id, "sub_yes"), fs.send)
		}
		b.HandleUpdate(cb(id, decline), fs.send)
		if st.Count() != 0 || len(st.Subscribers()) != 0 {
			t.Errorf("%s: после отказа ничего сохраняться не должно", decline)
		}
	}
}

func TestPDConsentWithoutMarketingIgnored(t *testing.T) {
	b, st, fs := newTestBot(t)
	runQuiz(t, b, fs, 3, []int{0, 0, 0, 0, 0})
	b.HandleUpdate(cb(3, "pd_yes"), fs.send) // в обход шага подписки
	if st.Count() != 0 {
		t.Error("без согласия на рассылку профиль сохраняться не должен")
	}
}

func TestPhoneCollection(t *testing.T) {
	b, st, fs := newTestBot(t)
	b.cfg.CollectPhone = true
	const id int64 = 77
	runQuiz(t, b, fs, id, []int{2, 2, 2, 0, 1})
	subscribe(b, fs, id)
	if fs.last().opts.RequestContact == "" {
		t.Fatalf("ожидал кнопку «Поделиться номером», получил %+v", fs.last())
	}
	if st.Count() != 0 {
		t.Fatal("до ответа на запрос номера профиль не сохраняется")
	}

	// Чужой контакт не принимаем.
	b.HandleUpdate(telegramapi.Update{SenderID: id, Contact: &telegramapi.Contact{Phone: "79990000000", UserID: 1}}, fs.send)
	if st.Count() != 0 {
		t.Fatal("чужой номер сохраняться не должен")
	}

	b.HandleUpdate(telegramapi.Update{SenderID: id, Username: "u", Contact: &telegramapi.Contact{Phone: "79991112233", UserID: id}}, fs.send)
	p, ok := st.Get(id)
	if !ok || p.Phone != "+79991112233" {
		t.Fatalf("номер не сохранён: %+v", p)
	}
}

func TestPhoneSkip(t *testing.T) {
	b, st, fs := newTestBot(t)
	b.cfg.CollectPhone = true
	runQuiz(t, b, fs, 78, []int{2, 2, 2, 0, 1})
	subscribe(b, fs, 78)
	b.HandleUpdate(text(78, "Пропустить"), fs.send)
	if p, ok := st.Get(78); !ok || p.Phone != "" {
		t.Fatalf("ожидал профиль без телефона, получил %+v, %v", p, ok)
	}
}

func TestPhotosAttached(t *testing.T) {
	b, _, fs := newTestBot(t)
	b.cfg.WelcomePhoto = "welcome.png"
	b.cfg.QuestionPhotos = []string{"q1.jpg", "q2.jpg", "q3.jpg", "q4.jpg", "q5.jpg"}
	b.HandleUpdate(text(1, "/start"), fs.send)
	if fs.last().opts.Photo != "welcome.png" {
		t.Errorf("приветствие без картинки: %+v", fs.last().opts)
	}
	b.HandleUpdate(cb(1, "start_test"), fs.send)
	for q := 1; q <= 5; q++ {
		want := b.cfg.QuestionPhotos[q-1]
		if got := fs.last().opts.Photo; got != want {
			t.Errorf("вопрос %d: картинка %q, хотим %q", q, got, want)
		}
		if !strings.Contains(fs.last().text, "Вопрос ") {
			t.Errorf("вопрос %d: нет индикатора прогресса", q)
		}
		if q < 5 {
			b.HandleUpdate(cb(1, "skip"), fs.send)
		}
	}
}

func TestMalformedCallbackDoesNotPanic(t *testing.T) {
	b, _, fs := newTestBot(t)
	b.HandleUpdate(cb(1, "q:5"), fs.send)
	b.HandleUpdate(cb(1, "q:"), fs.send)
	b.HandleUpdate(cb(1, "agree"), fs.send)
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

	if !strings.Contains(fs.last().text, "Ваш результат") {
		t.Errorf("ожидал результат теста, получил %q", fs.last().text)
	}
	if st.Count() != 0 {
		t.Error("результат не должен требовать сохранения данных")
	}
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

	resultMsg := fs.last().text
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
	if strings.Contains(fs.last().text, "Подписчиков") {
		t.Error("/stats не должен работать для не-админа")
	}

	// Админ: справка, статистика, список.
	b.HandleUpdate(text(admin, "/admin"), fs.send)
	if !strings.Contains(fs.last().text, "Команды администратора") {
		t.Errorf("ожидал админ-справку, получил %q", fs.last().text)
	}
	b.HandleUpdate(text(admin, "/stats"), fs.send)
	if !strings.Contains(fs.last().text, "Подписчиков с согласием: 0") {
		t.Errorf("ожидал нулевую статистику, получил %q", fs.last().text)
	}

	// Создадим профиль вручную и запустим рассылку.
	now := time.Now().UTC()
	if err := st.Save(store.Profile{TelegramID: user, Username: "@user", Headline: "X", ConsentVersion: "v", MarketingConsentAt: now}); err != nil {
		t.Fatal(err)
	}
	// Дал согласие на ПДн, но не на рассылку — получать не должен.
	if err := st.Save(store.Profile{TelegramID: 333, ConsentVersion: "v"}); err != nil {
		t.Fatal(err)
	}

	b.HandleUpdate(text(admin, "/broadcast"), fs.send)
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
	if fs.countTo(333) != 0 {
		t.Error("рассылка ушла пользователю без согласия на рассылку")
	}
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

func TestResubscribedUserResultUpdated(t *testing.T) {
	b, st, fs := newTestBot(t)
	const id int64 = 90
	runQuiz(t, b, fs, id, []int{0, 0, 0, 0, 0})
	subscribe(b, fs, id)
	runQuiz(t, b, fs, id, []int{1, 1, 1, 1, 1})
	b.HandleUpdate(cb(id, "practice"), fs.send)
	p, _ := st.Get(id)
	if p.Scores["B"] != 5 || p.Runs != 2 {
		t.Fatalf("результат подписчика не обновился: %+v", p)
	}
	if strings.Contains(fs.last().text, "Присылать?") {
		t.Error("подписчику не нужно снова предлагать подписку")
	}
}

func TestResultPhotos(t *testing.T) {
	cases := []struct {
		name    string
		choices []int
		want    string
	}{
		{"преобладает 1", []int{0, 0, 0, 1, 2}, "r1.jpg"},
		{"преобладает 2", []int{1, 1, 1, 0, 2}, "r2.jpg"},
		{"преобладает 3", []int{2, 2, 2, 0, 1}, "r3.jpg"},
		{"равенство", []int{0, 0, 1, 1, 2}, "tie.jpg"},
	}
	for _, tc := range cases {
		b, _, fs := newTestBot(t)
		b.cfg.ResultPhotos = []string{"r1.jpg", "r2.jpg", "r3.jpg"}
		b.cfg.ResultTiePhoto = "tie.jpg"
		runQuiz(t, b, fs, 1, tc.choices)
		if got := fs.last().opts.Photo; got != tc.want {
			t.Errorf("%s: картинка %q, хотим %q", tc.name, got, tc.want)
		}
	}
}

func hasCallback(m msg, data string) bool {
	for _, row := range m.opts.InlineKeyboard {
		for j := 1; j < len(row); j += 2 {
			if row[j] == data {
				return true
			}
		}
	}
	return false
}

func TestButtonsLayout(t *testing.T) {
	b, _, fs := newTestBot(t)
	b.HandleUpdate(text(1, "/start"), fs.send)
	if hasCallback(fs.last(), "consent_info") {
		t.Error("на приветствии не должно быть кнопки про данные")
	}
	b.HandleUpdate(cb(1, "start_test"), fs.send)
	if hasCallback(fs.last(), "skip") {
		t.Error("кнопки «Ничего из этого» быть не должно")
	}
	runQuiz(t, b, fs, 1, []int{2, 2, 2, 0, 1})
	b.HandleUpdate(cb(1, "practice"), fs.send)
	if !hasCallback(fs.last(), "consent_info") {
		t.Error("на последнем экране должна быть кнопка «Как я обращаюсь с данными»")
	}
}

func TestPracticeAudioSent(t *testing.T) {
	b, _, fs := newTestBot(t)
	b.cfg.PracticeAudio = "practice.mp3"
	runQuiz(t, b, fs, 1, []int{2, 2, 2, 0, 1}) // результат «Начать свою паузу сейчас»
	if !strings.Contains(fs.last().opts.InlineKeyboard[0][0], "Начать свою паузу сейчас") {
		t.Fatalf("ожидал кнопку «Начать свою паузу сейчас», получил %v", fs.last().opts.InlineKeyboard)
	}
	b.HandleUpdate(cb(1, "practice"), fs.send)
	audio := fs.sent[len(fs.sent)-2]
	if audio.opts.Audio != "practice.mp3" || audio.opts.AudioTitle == "" {
		t.Fatalf("аудио не отправлено: %+v", audio.opts)
	}
}

func TestHelpSeparatesAdmin(t *testing.T) {
	b, _, fs := newTestBot(t)
	b.HandleUpdate(text(222, "/help"), fs.send)
	if strings.Contains(fs.last().text, "/broadcast") {
		t.Error("пользователь не должен видеть админ-команды")
	}
	b.HandleUpdate(text(111, "/help"), fs.send)
	if !strings.Contains(fs.last().text, "/broadcast") {
		t.Error("админ должен видеть админ-команды")
	}
	b.HandleUpdate(text(222, "/broadcast"), fs.send)
	if strings.Contains(fs.last().text, "Рассылка") {
		t.Error("пользователь не должен запускать рассылку")
	}
}
