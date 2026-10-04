// Package bot — Telegram-слой: сценарий теста, согласия и админ-команды.
//
// Сценарий по ТЗ и 152-ФЗ:
//  1. Приветствие с картинкой → 5 вопросов, у каждого своя картинка.
//  2. Результат показывается сразу, без запроса каких-либо данных.
//  3. Практика выдаётся по кнопке прямо в чат — контакт для доставки не нужен.
//  4. Только после этого — необязательное предложение подписаться на материалы:
//     отдельное согласие на рассылку → отдельное согласие на обработку ПДн →
//     (по настройке) номер телефона кнопкой Telegram.
//  5. Отказ на любом шаге — в файл ничего не пишется.
package bot

import (
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/restquiz/rest-test-bot/internal/config"
	"github.com/restquiz/rest-test-bot/internal/quiz"
	"github.com/restquiz/rest-test-bot/internal/store"
	"github.com/restquiz/rest-test-bot/internal/telegramapi"
)

// Sender — функция отправки сообщения; подменяется в тестах.
type Sender func(chatID int64, text string, opts telegramapi.SendOptions) error

// Acker подтверждает нажатия inline-кнопок (реализация — telegramapi.API).
type Acker interface {
	AnswerCallback(u telegramapi.Update, alert string)
}

// Bot — обработчик входящих обновлений.
type Bot struct {
	cfg   *config.Config
	store *store.Store
	acker Acker

	mu        sync.Mutex
	states    map[int64]*state
	broadcast map[int64]*broadcastDraft
}

// state — текущая стадия диалога пользователя (только в памяти).
type state struct {
	stage    string
	question int // номер текущего вопроса (1-based)
	answers  quiz.Answers
	headline string
	scores   map[string]int
	// marketingAt — когда пользователь нажал «Да, присылайте».
	marketingAt time.Time
	// consentAt — когда пользователь нажал «Даю согласие».
	consentAt time.Time
}

type broadcastDraft struct {
	step string // "text" | "confirm"
	text string
}

const (
	stageQuiz    = "quiz"
	stageResult  = "result"  // результат показан, ждём кнопку практики
	stageOffer   = "offer"   // предложили подписку
	stageConsent = "consent" // ждём согласие на обработку ПДн
	stagePhone   = "phone"   // ждём номер телефона (если COLLECT_PHONE)

	callbackStart       = "start_test"
	callbackSkip        = "skip"
	callbackPractice    = "practice"
	callbackSubYes      = "sub_yes"
	callbackSubNo       = "sub_no"
	callbackPDYes       = "pd_yes"
	callbackPDNo        = "pd_no"
	callbackConsentInfo = "consent_info"
	callbackConsentShow = "consent_show"

	bcCancel = "bc_cancel"
	bcSend   = "bc_send"

	buttonSkipPhone = "Пропустить"
	consentMethod   = "telegram_inline_button"

	unsubscribeFooter = "\n\n—\nОтписаться и удалить данные: /forget"
)

// New создаёт бота. acker может быть nil (тогда подтверждения кнопок не отправляются).
func New(cfg *config.Config, st *store.Store, acker Acker) *Bot {
	return &Bot{
		cfg:       cfg,
		store:     st,
		acker:     acker,
		states:    make(map[int64]*state),
		broadcast: make(map[int64]*broadcastDraft),
	}
}

func (b *Bot) answerCallback(u telegramapi.Update, alert string) {
	if b.acker != nil {
		b.acker.AnswerCallback(u, alert)
	}
}

// HandleUpdate обрабатывает одно входящее событие. Паника в обработчике
// не роняет бота целиком.
func (b *Bot) HandleUpdate(u telegramapi.Update, send Sender) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[bot] паника при обработке %d: %v\n%s", u.SenderID, r, debug.Stack())
		}
	}()
	switch {
	case u.IsCallback():
		b.handleCallback(u, send)
	case u.Contact != nil:
		b.handleContact(u, send)
	default:
		b.handleText(u, send)
	}
}

func (b *Bot) getState(id int64) *state {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.states[id]
}

func (b *Bot) setState(id int64, st *state) {
	b.mu.Lock()
	b.states[id] = st
	b.mu.Unlock()
}

func (b *Bot) clearState(id int64) {
	b.mu.Lock()
	delete(b.states, id)
	b.mu.Unlock()
}

// ---------- текст ----------

func (b *Bot) handleText(u telegramapi.Update, send Sender) {
	text := strings.TrimSpace(u.Text)
	lower := strings.ToLower(text)
	isCommand := strings.HasPrefix(text, "/")

	b.mu.Lock()
	draft := b.broadcast[u.SenderID]
	b.mu.Unlock()
	st := b.getState(u.SenderID)

	if draft != nil && !isCommand {
		b.continueBroadcast(u.SenderID, draft, text, send)
		return
	}
	if st != nil && st.stage == stageQuiz && !isCommand {
		if choice, ok := parseAnswer(text); ok {
			b.answer(u.SenderID, st, choice, send)
			return
		}
	}
	if st != nil && st.stage == stagePhone && !isCommand {
		if lower == strings.ToLower(buttonSkipPhone) {
			b.saveProfile(u, st, "", send)
			return
		}
		b.askPhone(u.SenderID, send, "Чтобы поделиться номером, нажмите кнопку ниже — вводить его вручную не нужно. Или нажмите «Пропустить».")
		return
	}

	switch {
	case lower == "/start" || lower == "start":
		b.sendWelcome(u.SenderID, send)
	case lower == "/consent" || lower == "/privacy":
		b.showConsentSummary(u.SenderID, send, false)
	case lower == "отозвать согласие" || strings.HasPrefix(lower, "/forget") || strings.HasPrefix(lower, "/unsubscribe"):
		b.forget(u.SenderID, send)
	case strings.HasPrefix(lower, "/help"):
		b.reply(send, u.SenderID, helpText(b.cfg.IsAdmin(u.SenderID)))
	case strings.HasPrefix(lower, "/test"):
		b.startQuiz(u.SenderID, send)
	case strings.HasPrefix(lower, "/mystats") || strings.HasPrefix(lower, "/mydata"):
		b.showMyData(u.SenderID, send)
	case lower == "/cancel":
		b.mu.Lock()
		delete(b.broadcast, u.SenderID)
		b.mu.Unlock()
		b.clearState(u.SenderID)
		b.send(send, u.SenderID, "Отменил текущее действие. /start — чтобы начать заново.", telegramapi.SendOptions{RemoveKeyboard: true})
	case b.cfg.IsAdmin(u.SenderID) && isCommand:
		switch {
		case strings.HasPrefix(lower, "/admin"):
			b.reply(send, u.SenderID, adminHelpText)
		case strings.HasPrefix(lower, "/stats"):
			b.adminStats(send, u.SenderID)
		case strings.HasPrefix(lower, "/users"):
			b.adminUsers(send, u.SenderID)
		case strings.HasPrefix(lower, "/broadcast"):
			b.startBroadcast(u.SenderID, send)
		case strings.HasPrefix(lower, "/send"):
			b.adminSendDirect(u.SenderID, text, send)
		default:
			b.reply(send, u.SenderID, "Не понял команду. Напишите /admin.")
		}
	default:
		b.reply(send, u.SenderID, "Напишите /start, чтобы пройти тест.")
	}
}

// ---------- inline-кнопки ----------

func (b *Bot) handleCallback(u telegramapi.Update, send Sender) {
	data := u.Callback
	st := b.getState(u.SenderID)
	b.mu.Lock()
	draft := b.broadcast[u.SenderID]
	b.mu.Unlock()

	alert := ""
	defer func() { b.answerCallback(u, alert) }()

	switch {
	case data == callbackStart:
		b.startQuiz(u.SenderID, send)
	case strings.HasPrefix(data, "q:"):
		parts := strings.Split(strings.TrimPrefix(data, "q:"), ":")
		if len(parts) != 2 {
			return
		}
		qnum, err1 := strconv.Atoi(parts[0])
		opt, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil || opt < 0 || opt > 2 {
			return
		}
		if st == nil || st.stage != stageQuiz || st.question != qnum {
			alert = "Этот вопрос уже неактуален. Напишите /test, чтобы начать заново."
			return
		}
		b.answer(u.SenderID, st, opt, send)
	case data == callbackSkip:
		if st == nil || st.stage != stageQuiz {
			return
		}
		b.answer(u.SenderID, st, -1, send)
	case data == callbackPractice:
		b.deliverPractice(u.SenderID, st, send)
	case data == callbackSubYes:
		if st == nil {
			st = &state{}
			b.setState(u.SenderID, st)
		}
		st.stage = stageConsent
		st.marketingAt = time.Now().UTC()
		b.showConsentSummary(u.SenderID, send, true)
	case data == callbackSubNo:
		b.clearState(u.SenderID)
		b.reply(send, u.SenderID, "Хорошо, ничего не сохраняю. Пройти тест ещё раз — /test.")
	case data == callbackPDYes:
		if st == nil || st.stage != stageConsent || st.marketingAt.IsZero() {
			alert = "Сейчас это не актуально. Напишите /start."
			return
		}
		st.consentAt = time.Now().UTC()
		if b.cfg.CollectPhone {
			st.stage = stagePhone
			b.askPhone(u.SenderID, send, "Спасибо! Если хотите, поделитесь номером телефона — это необязательно, материалы всё равно будут приходить сюда, в Telegram.")
			return
		}
		b.saveProfile(u, st, "", send)
	case data == callbackPDNo:
		b.clearState(u.SenderID)
		b.reply(send, u.SenderID, "Хорошо, ничего не сохраняю. Без согласия я не могу присылать материалы, но практика остаётся у вас. Передумаете — /start.")
	case data == callbackConsentInfo:
		b.showConsentSummary(u.SenderID, send, false)
	case data == callbackConsentShow:
		b.showConsentFull(u.SenderID, send)
	case strings.HasPrefix(data, "bc:"):
		b.handleBroadcastCallback(u.SenderID, strings.TrimPrefix(data, "bc:"), draft, send)
	default:
		// Кнопки из старых версий бота (agree, decline, default_login...).
		alert = "Эта кнопка устарела. Напишите /start."
	}
}

// ---------- контакт ----------

func (b *Bot) handleContact(u telegramapi.Update, send Sender) {
	st := b.getState(u.SenderID)
	if st == nil || st.stage != stagePhone {
		b.send(send, u.SenderID, "Номер сейчас не нужен — я его не сохранил.", telegramapi.SendOptions{RemoveKeyboard: true})
		return
	}
	// Принимаем только собственный номер: чужие контакты без согласия их
	// владельца обрабатывать нельзя.
	if u.Contact.UserID != u.SenderID {
		b.askPhone(u.SenderID, send, "Можно поделиться только своим номером — кнопкой ниже. Или нажмите «Пропустить».")
		return
	}
	b.saveProfile(u, st, normalizePhone(u.Contact.Phone), send)
}

func normalizePhone(p string) string {
	p = strings.TrimSpace(p)
	if p != "" && !strings.HasPrefix(p, "+") {
		p = "+" + p
	}
	return p
}

// ---------- сценарий теста ----------

const welcomeText = "*Тест «Что не даёт вам нормально отдыхать вечером?»*\n\n" +
	"Узнайте, что чаще мешает вам переключиться после работы. И что можно попробовать уже сегодня, чтобы вечер не прошёл мимо вас.\n\n" +
	"Рабочий день закончился, но вы всё ещё решаете задачи в голове? Берёте телефон на минуту, и незаметно проходит час? Или добираетесь до отдыха, когда сил хватает только упасть на диван?\n\n" +
	"Ответьте на *5 коротких вопросов*. По вашим ответам вы:\n" +
	"• *разберётесь, где чаще застреваете:* в рабочих мыслях, бесконечном переключении между занятиями или привычке откладывать отдых;\n" +
	"• *получите понятный первый шаг* — без советов «просто расслабьтесь»;\n" +
	"• *заберёте бесплатную аудиопрактику «Переключатель»* — восемь минут с голосовым сопровождением.\n\n" +
	"_Выбирайте ответы, которые ближе к вашим обычным рабочим вечерам. Это тест для самонаблюдения, а не диагностика._"

func (b *Bot) sendWelcome(id int64, send Sender) {
	opts := telegramapi.SendOptions{
		Markdown: true,
		Photo:    b.cfg.WelcomePhoto,
		InlineKeyboard: [][]string{
			{"Узнать, что мешает мне отдыхать", callbackStart},
			{"🔒 Как я обращаюсь с данными", callbackConsentInfo},
		},
	}
	b.send(send, id, welcomeText, opts)
}

func (b *Bot) startQuiz(id int64, send Sender) {
	st := &state{stage: stageQuiz, question: 1, answers: quiz.Answers{}}
	b.setState(id, st)
	b.askQuestion(id, st, send)
}

func (b *Bot) askQuestion(id int64, st *state, send Sender) {
	q, ok := quiz.ByNumber(st.question)
	if !ok {
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Вопрос %d из %d\n\n*%s*\n\n", st.question, quiz.QuestionCount, q.Prompt)
	for i, opt := range q.Options {
		fmt.Fprintf(&sb, "%d. %s\n\n", i+1, opt.Text)
	}

	rows := make([][]string, 0, 2)
	answerRow := make([]string, 0, 6)
	for i := range q.Options {
		answerRow = append(answerRow, quiz.Label(i), fmt.Sprintf("q:%d:%d", q.Number, i))
	}
	rows = append(rows, answerRow, []string{"Ничего из этого / пропустить", callbackSkip})

	b.send(send, id, strings.TrimSpace(sb.String()), telegramapi.SendOptions{
		Markdown:       true,
		Photo:          b.cfg.QuestionPhoto(q.Number),
		InlineKeyboard: rows,
	})
}

// answer: choice 0..2 — вариант, -1 — пропуск.
func (b *Bot) answer(id int64, st *state, choice int, send Sender) {
	if choice >= 0 {
		st.answers[st.question] = choice
	}
	if st.question < quiz.QuestionCount {
		st.question++
		b.askQuestion(id, st, send)
		return
	}

	b.store.IncTests()
	outcome := quiz.Evaluate(st.answers)
	if outcome.Insufficient {
		b.clearState(id)
		b.reply(send, id, quiz.InsufficientCopy+"\n\nНапишите /test, чтобы попробовать ещё раз.")
		return
	}

	headlines := make([]string, 0, 2)
	for _, c := range outcome.Categories {
		headlines = append(headlines, quiz.Results[c].Title)
	}
	scores := make(map[string]int, len(outcome.Scores))
	for cat, v := range outcome.Scores {
		scores[string(cat)] = v
	}
	st.headline = strings.Join(headlines, " + ")
	st.scores = scores
	st.stage = stageResult

	b.sendResult(id, outcome, send)
}

func (b *Bot) sendResult(id int64, outcome quiz.Outcome, send Sender) {
	var sb strings.Builder
	writeCopy := func(cp quiz.ResultCopy) {
		sb.WriteString("*«" + cp.Title + "»*\n\n")
		for _, line := range cp.Lead {
			sb.WriteString(line + "\n\n")
		}
		for _, bullet := range cp.Bullets {
			sb.WriteString("• " + bullet + "\n")
		}
		sb.WriteString("\n" + cp.PracticeNote + "\n\n")
	}

	button := "Попробовать восьмиминутную практику"
	footer := "Бесплатно · 8 минут · Без специальной подготовки"
	if outcome.IsTie() {
		sb.WriteString("🔎 *Ваш результат*\n\n*" + quiz.TieCopy.Title + "*\n\n")
		for _, line := range quiz.TieCopy.Lead {
			sb.WriteString(line + "\n\n")
		}
		for _, c := range outcome.Categories {
			writeCopy(quiz.Results[c])
		}
	} else {
		cp := quiz.Results[outcome.Categories[0]]
		sb.WriteString("🔎 *Ваш результат*\n\n")
		writeCopy(cp)
		button, footer = cp.ButtonLabel, cp.Footer
	}
	sb.WriteString("_" + footer + "_")

	b.send(send, id, sb.String(), telegramapi.SendOptions{
		Markdown:       true,
		InlineKeyboard: [][]string{{"🎧 " + button, callbackPractice}},
	})
}

// deliverPractice присылает практику прямо в чат. Для этого достаточно
// Telegram ID текущего диалога — никакие данные не сохраняются.
func (b *Bot) deliverPractice(id int64, st *state, send Sender) {
	const title = "🎧 «Переключатель: 8 минут, чтобы перейти от дел к отдыху»"
	if b.cfg.PracticeAudio != "" {
		b.send(send, id, title+"\n\nУдобно сядьте и включите запись.", telegramapi.SendOptions{Audio: b.cfg.PracticeAudio})
	} else {
		b.reply(send, id, title+"\n\n"+quiz.PracticeURL)
	}

	// Уже подписан — повторно согласие не спрашиваем.
	if p, ok := b.store.Get(id); ok && p.Subscribed() {
		b.clearState(id)
		return
	}
	if st == nil {
		st = &state{}
		b.setState(id, st)
	}
	st.stage = stageOffer
	b.send(send, id,
		"Если захотите, я могу иногда присылать сюда новые короткие практики и материалы про вечерний отдых. "+
			"Это необязательно — практика уже ваша.\n\nПрисылать?",
		telegramapi.SendOptions{InlineKeyboard: [][]string{
			{"✅ Да, присылайте", callbackSubYes},
			{"Нет, спасибо", callbackSubNo},
		}})
}

// ---------- согласие и сохранение ----------

func (b *Bot) fillConsent(text string) string {
	operator := "администратор бота"
	contact := "сообщение в этот чат"
	policy := "по запросу у оператора"
	city := "—"
	if b.cfg != nil {
		if b.cfg.OperatorName != "" {
			operator = b.cfg.OperatorName
		}
		if b.cfg.OperatorContact != "" {
			contact = b.cfg.OperatorContact
		}
		if b.cfg.PolicyURL != "" {
			policy = b.cfg.PolicyURL
		}
		if b.cfg.OperatorCity != "" {
			city = b.cfg.OperatorCity
		}
	}
	return strings.NewReplacer(
		"{OPERATOR}", operator,
		"{OPERATOR_CONTACT}", contact,
		"{POLICY_URL}", policy,
		"{CITY}", city,
		"{RETENTION}", b.retentionText(),
		"{VERSION}", quiz.ConsentVersion,
	).Replace(text)
}

func (b *Bot) retentionText() string {
	if b.cfg == nil || b.cfg.Retention <= 0 {
		return "до отзыва согласия"
	}
	days := int(b.cfg.Retention.Hours() / 24)
	if days%365 == 0 {
		years := days / 365
		if years == 1 {
			return "1 год"
		}
		return fmt.Sprintf("%d г.", years)
	}
	return fmt.Sprintf("%d дн.", days)
}

// showConsentSummary показывает краткое согласие. withButtons — показать
// кнопки «Даю согласие / Не даю» (шаг подписки), иначе — справка.
func (b *Bot) showConsentSummary(id int64, send Sender, withButtons bool) {
	rows := [][]string{{"📃 Полный текст согласия", callbackConsentShow}}
	if withButtons {
		rows = [][]string{
			{"✅ Даю согласие на обработку данных", callbackPDYes},
			{"📃 Полный текст согласия", callbackConsentShow},
			{"❌ Не даю", callbackPDNo},
		}
	}
	b.send(send, id, b.fillConsent(quiz.ConsentSummary), telegramapi.SendOptions{InlineKeyboard: rows})
}

func (b *Bot) showConsentFull(id int64, send Sender) {
	for _, chunk := range splitChunks(b.fillConsent(quiz.ConsentFullText), 4000) {
		if err := send(id, chunk, telegramapi.SendOptions{}); err != nil {
			log.Printf("[bot] отправка полного согласия %d: %v", id, err)
			return
		}
	}
}

func (b *Bot) askPhone(id int64, send Sender, text string) {
	b.send(send, id, text, telegramapi.SendOptions{
		RequestContact:  "📱 Поделиться номером",
		ReplyKeyboard:   []string{buttonSkipPhone},
		OneTimeKeyboard: true,
	})
}

// saveProfile сохраняет профиль: только после двух явных согласий.
func (b *Bot) saveProfile(u telegramapi.Update, st *state, phone string, send Sender) {
	if st.marketingAt.IsZero() || st.consentAt.IsZero() {
		b.reply(send, u.SenderID, "Не вижу вашего согласия — начните заново: /start.")
		b.clearState(u.SenderID)
		return
	}
	username := ""
	if u.Username != "" {
		username = "@" + u.Username
	}
	profile := store.Profile{
		TelegramID:         u.SenderID,
		Username:           username,
		FirstName:          u.FirstName,
		Phone:              phone,
		ConsentedAt:        st.consentAt,
		ConsentVersion:     quiz.ConsentVersion,
		ConsentMethod:      consentMethod,
		MarketingConsentAt: st.marketingAt,
		Scores:             st.scores,
		Headline:           st.headline,
	}
	if err := b.store.Save(profile); err != nil {
		log.Printf("[bot] сохранение профиля %d: %v", u.SenderID, err)
		b.send(send, u.SenderID, "Не получилось сохранить данные, попробуйте позже.", telegramapi.SendOptions{RemoveKeyboard: true})
		return
	}
	b.clearState(u.SenderID)
	b.send(send, u.SenderID, "Готово! Буду иногда присылать сюда новые материалы.\n\n"+
		"Что я о вас храню — /mydata.\nОтписаться и удалить все данные — /forget, в любой момент.",
		telegramapi.SendOptions{RemoveKeyboard: true})
}

// showMyData — право субъекта на доступ к своим данным (ст. 14 152-ФЗ).
func (b *Bot) showMyData(id int64, send Sender) {
	p, ok := b.store.Get(id)
	if !ok {
		b.reply(send, id, "Я ничего о вас не храню.")
		return
	}
	var sb strings.Builder
	sb.WriteString("Что я о вас храню:\n\n")
	fmt.Fprintf(&sb, "Telegram ID: %d\n", p.TelegramID)
	fmt.Fprintf(&sb, "Имя: %s\n", orDash(p.FirstName))
	fmt.Fprintf(&sb, "Логин: %s\n", orDash(p.Username))
	fmt.Fprintf(&sb, "Телефон: %s\n", orDash(p.Phone))
	fmt.Fprintf(&sb, "Результат теста: %s\n", orDash(p.Headline))
	if len(p.Scores) > 0 {
		var parts []string
		for _, c := range quiz.AllCategories {
			if v := p.Scores[string(c)]; v > 0 {
				parts = append(parts, fmt.Sprintf("%s: %d", quiz.CategoryNames[c], v))
			}
		}
		if len(parts) > 0 {
			sb.WriteString("Баллы: " + strings.Join(parts, ", ") + "\n")
		}
	}
	fmt.Fprintf(&sb, "Согласие дано: %s (редакция %s)\n", p.ConsentedAt.Format("02.01.2006 15:04 UTC"), orDash(p.ConsentVersion))
	sb.WriteString("\nУдалить всё и отписаться — /forget.")
	b.reply(send, id, sb.String())
}

// forget — отзыв согласия: профиль удаляется сразу (ст. 21 152-ФЗ).
func (b *Bot) forget(id int64, send Sender) {
	b.clearState(id)
	if _, ok := b.store.Get(id); !ok {
		b.send(send, id, "Я ничего о вас не храню — удалять нечего.", telegramapi.SendOptions{RemoveKeyboard: true})
		return
	}
	if err := b.store.Delete(id); err != nil {
		log.Printf("[bot] удаление профиля %d: %v", id, err)
		b.reply(send, id, "Не удалось удалить данные, попробуйте ещё раз или напишите оператору.")
		return
	}
	b.send(send, id, "Согласие отозвано, все ваши данные удалены, рассылки больше не будет.", telegramapi.SendOptions{RemoveKeyboard: true})
}

// ---------- админ ----------

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

// ---------- хелперы ----------

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
