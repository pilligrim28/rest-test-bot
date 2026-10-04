package bot

import (
        "fmt"
        "log"
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

// Bot — обработчик входящих обновлений.
type Bot struct {
        cfg   *config.Config
        store *store.Store
        acker Acker

        mu        sync.Mutex
        states    map[int64]*state
        broadcast map[int64]*broadcastDraft
}

// Acker подтверждает нажатия inline-кнопок (реализация — telegramapi.API).
type Acker interface {
        AnswerCallback(u telegramapi.Update, alert string)
}

// state — текущая стадия диалога пользователя.
type state struct {
        stage       string // "" | "quiz" | "await_login" | "consent_asked"
        question    int    // номер текущего вопроса (1-based)
        answers     quiz.Answers
        headline    string // показанный результат (для подтверждения логина)
        scores      map[string]int
        practiceURL string
        optIn       bool // пользователь уже ответил на запрос согласия (кнопкой или текстом)
}

// broadcastDraft — черновик массовой рассылки в мастере /broadcast.
type broadcastDraft struct {
        step   string // "segment" | "text" | "confirm"
        seg    string // "consented" | "all"
        text   string
        preset string
}

const (
        stageQuiz       = "quiz"
        stageConsentAsk = "consent_asked"
        stageAwaitLogin = "await_login"

        callbackStart        = "start_test"
        callbackSkip         = "skip"
        callbackPractice     = "practice"
        callbackAgree        = "agree"
        callbackDecline      = "decline"
        callbackDefaultLogin = "default_login"
        // Согласие на обработку персональных данных (152-ФЗ)
        callbackConsentInfo  = "consent_info"
        callbackConsentShow  = "consent_show"
        callbackConsentPrint = "consent_print"

        bcAllConsented = "bc_all_consent"
        bcAllTested    = "bc_all_tested"
        bcCustom       = "bc_custom"
        bcCancel       = "bc_cancel"
        bcSend         = "bc_send"
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

// answerCallback безопасно подтверждает нажатие кнопки.
func (b *Bot) answerCallback(u telegramapi.Update, alert string) {
        if b.acker != nil {
                b.acker.AnswerCallback(u, alert)
        }
}

// HandleUpdate обрабатывает одно входящее событие.
func (b *Bot) HandleUpdate(u telegramapi.Update, send Sender) {
        if u.IsCallback() {
                b.handleCallback(u, send)
                return
        }
        b.handleText(u, send)
}

// ---------- текст ----------

func (b *Bot) handleText(u telegramapi.Update, send Sender) {
        text := strings.TrimSpace(u.Text)
        lower := strings.ToLower(text)

        b.mu.Lock()
        draft := b.broadcast[u.SenderID]
        st := b.states[u.SenderID]
        b.mu.Unlock()

        // Мастер рассылки перехватывает обычный текст (сообщение для отправки).
        if draft != nil && !strings.HasPrefix(text, "/") {
                b.continueBroadcast(u.SenderID, draft, text, send)
                return
        }

        // Ожидание подтверждения логина — принимаем любой нетекстовый ввод как логин.
        if st != nil && st.stage == stageAwaitLogin && !strings.HasPrefix(text, "/") {
                b.confirmLogin(u, st, text, send)
                return
        }

        // Ответы на вопросы тестом принимаются и текстом: 1/2/3, А/Б/В, Пропустить.
        if st != nil && st.stage == stageQuiz && !strings.HasPrefix(text, "/") {
                if choice, ok := parseAnswer(text); ok {
                        b.answer(u.SenderID, st, choice, send)
                        return
                }
        }

        // На запрос согласия можно ответить и текстом: «да» / «согласен» и т. п.
        if st != nil && st.stage == stageConsentAsk && !st.optIn && !strings.HasPrefix(text, "/") {
                switch lower {
                case "да", "yes", "ок", "окей", "хорошо", "согласна", "согласен", "соглашаюсь", "ну да", "давай":
                        st.optIn = true
                        b.askLogin(u.SenderID, st, send)
                        return
                case "нет", "no", "не надо", "не сохраняй", "не сохранять", "отказываюсь":
                        st.optIn = true
                        b.store.MarkOptIn(u.SenderID)
                        b.clearState(u.SenderID)
                        b.reply(send, u.SenderID, "Хорошо, ничего не сохраняю. Практика всё равно доступна: "+quiz.PracticeURL)
                        return
                }
        }

        switch {
        case lower == "/start" || lower == "start" || text == "🚀 Пройти тест":
                b.sendWelcome(u.SenderID, send)
        case text == "📄 Обработка персональных данных" || lower == "/consent":
                b.showConsentIntro(u.SenderID, send)
        case lower == "отозвать согласие":
                b.revokeConsent(u.SenderID, send)
        case strings.HasPrefix(lower, "/help"):
                b.reply(send, u.SenderID, helpText(b.cfg.IsAdmin(u.SenderID)))
        case strings.HasPrefix(lower, "/test"):
                b.startQuiz(u.SenderID, send)
        case strings.HasPrefix(lower, "/mystats"):
                b.showStats(u.SenderID, send)
        case strings.HasPrefix(lower, "/forget"):
                b.forget(u.SenderID, send)
        case lower == "/cancel":
                b.mu.Lock()
                delete(b.broadcast, u.SenderID)
                b.mu.Unlock()
                b.clearState(u.SenderID)
                b.reply(send, u.SenderID, "Отменил текущее действие. /start — чтобы начать заново.")
        case b.cfg.IsAdmin(u.SenderID):
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
                        if b.cfg.IsAdmin(u.SenderID) {
                                b.reply(send, u.SenderID, "Не понял команду. Напишите /help.")
                        } else {
                                b.reply(send, u.SenderID, "Напишите /start, чтобы пройти тест.")
                        }
                }
        default:
                b.reply(send, u.SenderID, "Напишите /start, чтобы пройти тест.")
        }
}

// ---------- inline-кнопки ----------

func (b *Bot) handleCallback(u telegramapi.Update, send Sender) {
        data := u.Callback

        b.mu.Lock()
        st := b.states[u.SenderID]
        draft := b.broadcast[u.SenderID]
        b.mu.Unlock()

        switch {
        case data == callbackStart:
                b.startQuiz(u.SenderID, send)
                b.answerCallback(u, "")
                return
        case strings.HasPrefix(data, "q:"):
                rest := strings.TrimPrefix(data, "q:")
                parts := strings.SplitN(rest, ":", 2)
                qnum, err1 := strconv.Atoi(parts[0])
                opt, err2 := strconv.Atoi(parts[1])
                if err1 != nil || err2 != nil {
                        b.answerCallback(u, "")
                        return
                }
                if st == nil || st.stage != stageQuiz || st.question != qnum {
                        b.reply(send, u.SenderID, "Этот вопрос уже неактуален. Напишите /test, чтобы начать заново.")
                        b.answerCallback(u, "")
                        return
                }
                b.answer(u.SenderID, st, opt, send)
                b.answerCallback(u, "")
                return
        case data == callbackSkip:
                if st == nil || st.stage != stageQuiz {
                        b.answerCallback(u, "")
                        return
                }
                b.answer(u.SenderID, st, -1, send)
                b.answerCallback(u, "")
                return
        case data == callbackPractice:
                if st != nil && st.practiceURL != "" {
                        b.reply(send, u.SenderID, "Практика здесь: "+st.practiceURL)
                }
                b.answerCallback(u, "")
                return
        case data == callbackConsentInfo:
                b.showConsentIntro(u.SenderID, send)
                b.answerCallback(u, "")
                return
        case data == callbackConsentShow:
                b.showConsentFull(u.SenderID, send)
                b.answerCallback(u, "")
                return
        case data == callbackConsentPrint:
                b.sendConsentFile(u.SenderID, send)
                b.answerCallback(u, "Отправляю текстом для копирования.")
                return
        case data == callbackAgree:
                if st == nil || st.stage != stageConsentAsk {
                        b.answerCallback(u, "Сейчас это не актуально.")
                        return
                }
                b.askLogin(u.SenderID, st, send)
                b.answerCallback(u, "")
                return
        case data == callbackDecline:
                if st == nil || st.stage != stageConsentAsk {
                        b.answerCallback(u, "")
                        return
                }
                b.store.MarkOptIn(u.SenderID)
                b.clearState(u.SenderID)
                b.reply(send, u.SenderID, "Хорошо, ничего не сохраняю. Практика всё равно доступна: "+quiz.PracticeURL)
                b.answerCallback(u, "")
                return
        case data == callbackDefaultLogin:
                if st == nil || st.stage != stageAwaitLogin {
                        b.answerCallback(u, "")
                        return
                }
                login := strings.TrimSpace(u.SenderName)
                if i := strings.Index(login, "(@"); i >= 0 {
                        login = login[i+2 : len(login)-1]
                }
                if login == "" {
                        b.answerCallback(u, "Не вижу вашего юзернейма — напишите логин сообщением.")
                        return
                }
                b.confirmLogin(u, st, login, send)
                b.answerCallback(u, "")
                return
        case strings.HasPrefix(data, "bc:"):
                b.handleBroadcastCallback(u.SenderID, strings.TrimPrefix(data, "bc:"), draft, send)
                b.answerCallback(u, "")
                return
        default:
                b.answerCallback(u, "")
        }
}

// ---------- сценарии ----------

func (b *Bot) sendWelcome(id int64, send Sender) {
        text := "Привет! Я помогу понять, что мешает вам нормально отдыхать вечером.\n\n" +
                "Короткий тест: 5 вопросов, около двух минут. В конце подскажу, какая привычка чаще всего крадёт ваш отдых, и дам бесплатную восьмиминутную практику."
        rows := [][]string{
                {"🚀 Пройти тест", callbackStart},
                {"📄 Обработка персональных данных", callbackConsentInfo},
        }
        opts := telegramapi.SendOptions{InlineKeyboard: rows}
        // Картинка к приветствию задаётся переменной WELCOME_PHOTO_URL в .env
        // (URL изображения или file_id). Если не задана — обычное текстовое сообщение.
        if b.cfg != nil && b.cfg.WelcomePhotoURL != "" {
                opts.Photo = b.cfg.WelcomePhotoURL
        }
        if err := send(id, text, opts); err != nil {
                log.Printf("[bot] отправка приветствия %d: %v", id, err)
        }
}

func (b *Bot) startQuiz(id int64, send Sender) {
        st := &state{stage: stageQuiz, question: 1, answers: quiz.Answers{}, practiceURL: quiz.PracticeURL}
        b.mu.Lock()
        b.states[id] = st
        b.mu.Unlock()
        b.askQuestion(id, st, send)
}

func (b *Bot) askQuestion(id int64, st *state, send Sender) {
        q, ok := quiz.ByNumber(st.question)
        if !ok {
                return
        }
        var sb strings.Builder
        fmt.Fprintf(&sb, "Вопрос %d из %d\n\n%s\n\n", st.question, quiz.QuestionCount, q.Prompt)
        for i, opt := range q.Options {
                fmt.Fprintf(&sb, "%d. %s\n", i+1, opt.Text)
        }
        sb.WriteString("\nМожно ответить кнопкой, цифрой (1–3) или буквой (А/Б/В). Если ничего не подходит — нажмите «Пропустить».")

        rows := make([][]string, 0, 4)
        for i, opt := range q.Options {
                rows = append(rows, []string{fmt.Sprintf("%d. %s", i+1, shortLabel(opt.Text)), fmt.Sprintf("q:%d:%d", q.Number, i)})
        }
        rows = append(rows, []string{"Ничего из этого / пропустить", callbackSkip})
        b.replyWithKeyboard(send, id, sb.String(), rows)
}

func shortLabel(text string) string {
        runes := []rune(text)
        if len(runes) > 48 {
                return string(runes[:47]) + "…"
        }
        return text
}

// choice: 0..2 — вариант, -1 — пропуск.
func (b *Bot) answer(id int64, st *state, choice int, send Sender) {
        if choice >= 0 {
                st.answers[st.question] = choice
        }

        if st.question < quiz.QuestionCount {
                st.question++
                b.askQuestion(id, st, send)
                return
        }

        outcome := quiz.Evaluate(st.answers)
        if outcome.Insufficient {
                b.clearState(id)
                b.reply(send, id, quiz.InsufficientCopy+"\n\nНапишите /test, чтобы попробовать ещё раз.")
                return
        }

        headlines := make([]string, 0, 2)
        scores := make(map[string]int, len(outcome.Categories))
        for _, c := range outcome.Categories {
                cp := quiz.Results[c]
                headlines = append(headlines, cp.Title)
                for cat, v := range outcome.Scores {
                        scores[string(cat)] = v
                }
        }
        st.headline = strings.Join(headlines, " + ")
        st.scores = scores

        b.sendResult(id, outcome, st, send)
}

func (b *Bot) sendResult(id int64, outcome quiz.Outcome, st *state, send Sender) {
        var sb strings.Builder
        if outcome.IsTie() {
                sb.WriteString("🔎 *Ваш результат*\n\n" + quiz.TieCopy.Title + "\n\n")
                for _, line := range quiz.TieCopy.Lead {
                        sb.WriteString(line + "\n\n")
                }
                for _, c := range outcome.Categories {
                        cp := quiz.Results[c]
                        sb.WriteString("*«" + cp.Title + "»*\n\n")
                        for _, line := range cp.Lead {
                                sb.WriteString(line + "\n\n")
                        }
                        for _, bullet := range cp.Bullets {
                                sb.WriteString("• " + bullet + "\n")
                        }
                        sb.WriteString("\n" + cp.PracticeNote + "\n\n")
                }
        } else {
                c := outcome.Categories[0]
                cp := quiz.Results[c]
                sb.WriteString("🔎 *Ваш результат*\n\n*" + cp.Title + "*\n\n")
                for _, line := range cp.Lead {
                        sb.WriteString(line + "\n\n")
                }
                for _, bullet := range cp.Bullets {
                        sb.WriteString("• " + bullet + "\n")
                }
                sb.WriteString("\n" + cp.PracticeNote + "\n\n")
        }
        sb.WriteString("_Бесплатно · 8 минут · Без специальной подготовки_")

        sendOpts := telegramapi.SendOptions{
                Markdown: true,
                InlineKeyboard: [][]string{
                        {"🎧 Включить практику", callbackPractice},
                },
        }
        if err := send(id, sb.String(), sendOpts); err != nil {
                log.Printf("[bot] не удалось отправить результат %d: %v", id, err)
        }

        // Согласие — отдельным сообщением, как требует ТЗ.
        consent := "Хотите, я сохраню ваш результат и буду иногда присылать полезные материалы? Нажмите «Согласен», если да.\n\n" +
                "Я сохраню: ваш Telegram ID, имя и подтверждённый логин, результат теста.\n" +
                "Не сохраняю: телефон, почту, геолокацию, переписку.\n" +
                "Отзыв согласия — команда /forget в любой момент.\n\n" +
                "Полный текст согласия на обработку персональных данных (152-ФЗ) — по кнопке ниже или командой /consent."
        b.replyWithKeyboard(send, id, consent, [][]string{
                {"✅ Согласен на обработку данных", callbackAgree},
                {"❌ Не сохранять", callbackDecline},
                {"📄 Показать согласие (152-ФЗ)", callbackConsentInfo},
        })

        // Состояние сохраняем до ответа пользователя: согласие можно выразить
        // не только кнопкой, но и текстом («да», «согласен») — см. handleText.
        st.stage = stageConsentAsk
}

func (b *Bot) askLogin(id int64, st *state, send Sender) {
        st.stage = stageAwaitLogin
        text := "Отлично! Подтвердите, пожалуйста, ваш логин в Telegram — он будет указан в письмах и рассылках.\n\n" +
                "Нажмите кнопку, если всё верно, или напишите свой логин текстом (например, @username)."
        rows := [][]string{{"Подтвердить этот логин", callbackDefaultLogin}}
        b.replyWithKeyboard(send, id, text, rows)
}

func (b *Bot) confirmLogin(u telegramapi.Update, st *state, login string, send Sender) {
        login = strings.TrimSpace(login)
        if login == "" {
                b.reply(send, u.SenderID, "Логин получился пустым — попробуйте ещё раз или напишите @имя.")
                return
        }
        if !strings.HasPrefix(login, "@") {
                login = "@" + login
        }
        profile := store.Profile{
                TelegramID: u.SenderID,
                Username:   login,
                Login:      login,
                FirstName:  strings.TrimSpace(strings.SplitN(u.SenderName, " (", 2)[0]),
                Scores:     st.scores,
                Headline:   st.headline,
        }
        if err := b.store.Save(profile); err != nil {
                b.reply(send, u.SenderID, "Не смог сохранить данные: "+err.Error())
                return
        }
        b.clearState(u.SenderID)
        b.reply(send, u.SenderID, "Готово! Сохранил вас как "+login+".\n\n"+
                "Ваши результаты — командой /mystats, удалить данные — /forget.\n\n"+
                "Практика здесь: "+quiz.PracticeURL)
}

func (b *Bot) showStats(id int64, send Sender) {
        p, ok := b.store.Get(id)
        if !ok {
                b.reply(send, id, "Я пока ничего о вас не сохранил. Пройдите тест (/test) и подтвердите согласие.")
                return
        }
        text := fmt.Sprintf("Прохождений: %d\nРезультат: %s\nСохранено: %s",
                p.Runs, orDash(p.Headline), p.ConsentedAt.Format("02.01.2006"))
        if len(p.Scores) > 0 {
                var parts []string
                for _, c := range quiz.AllCategories {
                        if v, ok := p.Scores[string(c)]; ok && v > 0 {
                                parts = append(parts, fmt.Sprintf("%s: %d", quiz.CategoryNames[c], v))
                        }
                }
                if len(parts) > 0 {
                        text += "\nБаллы: " + strings.Join(parts, ", ")
                }
        }
        b.reply(send, id, text)
}

func (b *Bot) forget(id int64, send Sender) {
        if _, ok := b.store.Get(id); !ok {
                b.reply(send, id, "Я ничего о вас не сохранял — удалять нечего.")
                return
        }
        if err := b.store.Delete(id); err != nil {
                b.reply(send, id, "Не удалось удалить данные: "+err.Error())
                return
        }
        b.clearState(id)
        b.reply(send, id, "Все ваши данные удалены. Если захотите — начните заново командой /test.")
}

// ---------- согласие на обработку персональных данных (152-ФЗ) ----------

// consentText рендерит полный текст согласия с реквизитами оператора из .env.
func (b *Bot) consentText() string {
        operator, city := "оператор персональных данных (администратор бота)", "Москва"
        if b.cfg != nil {
                if b.cfg.OperatorName != "" {
                        operator = b.cfg.OperatorName
                }
                if b.cfg.OperatorCity != "" {
                        city = b.cfg.OperatorCity
                }
        }
        t := strings.NewReplacer(
                "{OPERATOR}", operator,
                "{CITY}", city,
                "{DATE}", time.Now().Format("02 января 2006 г."),
        ).Replace(quiz.ConsentFullText)
        return t
}

// showConsentIntro — краткая шапка + кнопки «полный текст» / «текстом».
func (b *Bot) showConsentIntro(id int64, send Sender) {
        rows := [][]string{
                {"📃 Показать полный текст", callbackConsentShow},
                {"📋 Прислать текстом для копирования", callbackConsentPrint},
        }
        opts := telegramapi.SendOptions{InlineKeyboard: rows}
        if err := send(id, quiz.ConsentIntro, opts); err != nil {
                log.Printf("[bot] отправка согласия %d: %v", id, err)
        }
}

// showConsentFull — полный текст; Telegram лимит 4096 символов, поэтому при
// необходимости разбиваем на несколько сообщений по границам абзацев.
func (b *Bot) showConsentFull(id int64, send Sender) {
        for _, chunk := range splitChunks(b.consentText(), 4000) {
                if err := send(id, chunk, telegramapi.SendOptions{}); err != nil {
                        log.Printf("[bot] отправка полного согласия %d: %v", id, err)
                        return
                }
        }
}

// sendConsentFile — то же, что showConsentFull (текстом): в Telegram файлы
// документом отправляются отдельным методом, но текстовый вариант проще
// скопировать и распечатать, что и требуется пользователю.
func (b *Bot) sendConsentFile(id int64, send Sender) {
        b.showConsentFull(id, send)
}

// revokeConsent — отзыв согласия: удаляем профиль и все данные (ст. 21 152-ФЗ).
func (b *Bot) revokeConsent(id int64, send Sender) {
        b.forget(id, send)
}

// splitChunks режет текст на куски не длиннее max (в рунах — лимит Telegram
// считается в символах) по границам "\n\n".
func splitChunks(text string, max int) []string {
        var out []string
        cur := ""
        addPara := func(para string) {
                switch {
                case cur == "":
                        cur = para
                case len([]rune(cur))+2+len([]rune(para)) <= max:
                        cur += "\n\n" + para
                default:
                        out = append(out, cur)
                        cur = para
                }
                // Абзац сам длиннее лимита — режем жёстко по руне.
                for len([]rune(cur)) > max {
                        r := []rune(cur)
                        out = append(out, string(r[:max]))
                        cur = string(r[max:])
                }
        }
        for _, para := range strings.Split(text, "\n\n") {
                addPara(para)
        }
        if cur != "" {
                out = append(out, cur)
        }
        return out
}

func (b *Bot) clearState(id int64) {
        b.mu.Lock()
        delete(b.states, id)
        b.mu.Unlock()
}

// ---------- админ ----------

func (b *Bot) adminStats(send Sender, id int64) {
        profiles := b.store.List()
        counts := map[string]int{}
        totalRuns := 0
        for _, p := range profiles {
                totalRuns += p.Runs
                key := p.Headline
                if key == "" {
                        key = "(без результата)"
                }
                counts[key]++
        }
        var sb strings.Builder
        fmt.Fprintf(&sb, "Профилей с согласием: %d\nВсего прохождений: %d\nПрошли тест (всего): %d\n\nРаспределение результатов:\n",
                len(profiles), totalRuns, len(b.store.Recipients()))
        keys := make([]string, 0, len(counts))
        for k := range counts {
                keys = append(keys, k)
        }
        sortStrings(keys)
        for _, k := range keys {
                fmt.Fprintf(&sb, "• %s — %d\n", k, counts[k])
        }
        b.reply(send, id, sb.String())
}

func (b *Bot) adminUsers(send Sender, id int64) {
        profiles := b.store.List()
        if len(profiles) == 0 {
                b.reply(send, id, "Пока никто не давал согласие на обработку данных.")
                return
        }
        var sb strings.Builder
        fmt.Fprintf(&sb, "Всего профилей: %d\n\n", len(profiles))
        for i, p := range profiles {
                fmt.Fprintf(&sb, "%d. %s — %s (%d)\n", i+1, orDash(p.Login), orDash(p.Headline), p.TelegramID)
        }
        b.reply(send, id, truncate(sb.String(), 4000))
}

func (b *Bot) adminSendDirect(adminID int64, raw string, send Sender) {
        trimmed := strings.TrimSpace(raw)
        if len(trimmed) >= 5 {
                trimmed = trimmed[5:] // снимаем префикс "/send", регистр текста сохраняем
        }
        rest := strings.TrimSpace(trimmed)
        fields := strings.Fields(rest)
        if len(fields) < 2 {
                b.reply(send, adminID, "Формат: /send <Telegram ID> <текст сообщения>")
                return
        }
        target, err := strconv.ParseInt(fields[0], 10, 64)
        if err != nil {
                b.reply(send, adminID, "ID должен быть числом. Пример: /send 123456789 Привет!")
                return
        }
        msg := strings.Join(fields[1:], " ")
        if err := send(target, msg, telegramapi.SendOptions{}); err != nil {
                b.reply(send, adminID, fmt.Sprintf("Не отправилось пользователю %d: %v", target, err))
                return
        }
        b.reply(send, adminID, fmt.Sprintf("Отправлено пользователю %d.", target))
}

// ---------- рассылка (мастер) ----------

func (b *Bot) startBroadcast(id int64, send Sender) {
        draft := &broadcastDraft{step: "segment"}
        b.mu.Lock()
        b.broadcast[id] = draft
        b.mu.Unlock()

        consented := len(b.store.ConsentedRecipients())
        all := len(b.store.Recipients())
        text := fmt.Sprintf("Массовая рассылка. Выберите аудиторию:\n\n· Согласные на обработку данных: %d\n· Все, кто прошёл тест: %d\n\nИли отмените: /cancel", consented, all)
        rows := [][]string{
                {"Только согласившиеся (" + strconv.Itoa(consented) + ")", "bc:" + bcAllConsented},
                {"Все прошедшие тест (" + strconv.Itoa(all) + ")", "bc:" + bcAllTested},
                {"Отмена", "bc:" + bcCancel},
        }
        b.replyWithKeyboard(send, id, text, rows)
}

func (b *Bot) handleBroadcastCallback(id int64, action string, draft *broadcastDraft, send Sender) {
        if draft == nil && action != bcCancel {
                b.reply(send, id, "Мастер рассылки уже завершён. Начните заново: /broadcast")
                return
        }
        switch action {
        case bcCancel:
                b.mu.Lock()
                delete(b.broadcast, id)
                b.mu.Unlock()
                b.reply(send, id, "Рассылка отменена.")
        case bcAllConsented, bcAllTested:
                if action == bcAllTested {
                        draft.seg = "all"
                } else {
                        draft.seg = "consented"
                }
                draft.step = "text"
                b.reply(send, id, "Аудитория выбрана: "+segName(draft.seg)+".\n\nТеперь пришлите текст сообщения одним сообщением. Чтобы отменить — /cancel.")
        case bcSend:
                b.runBroadcast(id, draft, send)
        default:
                b.reply(send, id, "Неизвестное действие рассылки. Начните заново: /broadcast")
        }
}

func segName(seg string) string {
        if seg == "all" {
                return "все прошедшие тест"
        }
        return "только согласившиеся"
}

func (b *Bot) continueBroadcast(id int64, draft *broadcastDraft, text string, send Sender) {
        if draft.step != "text" {
                b.reply(send, id, "Сначала выберите аудиторию кнопкой. Или отмените: /cancel")
                return
        }
        draft.text = strings.TrimSpace(text)
        if draft.text == "" {
                b.reply(send, id, "Получилось пусто — пришлите текст ещё раз или /cancel.")
                return
        }
        draft.step = "confirm"
        n := b.recipientCount(draft.seg)
        preview := truncate(draft.text, 500)
        rows := [][]string{
                {"✅ Отправить " + strconv.Itoa(n) + " получателям", "bc:" + bcSend},
                {"Отмена", "bc:" + bcCancel},
        }
        b.replyWithKeyboard(send, id, "Предпросмотр:\n\n"+preview+"\n\nПолучателей: "+strconv.Itoa(n), rows)
}

func (b *Bot) recipientCount(seg string) int {
        if seg == "all" {
                return len(b.store.Recipients())
        }
        return len(b.store.ConsentedRecipients())
}

func (b *Bot) runBroadcast(adminID int64, draft *broadcastDraft, send Sender) {
        b.mu.Lock()
        delete(b.broadcast, adminID)
        b.mu.Unlock()

        recipients := b.store.ConsentedRecipients()
        if draft.seg == "all" {
                recipients = b.store.Recipients()
        }
        if len(recipients) == 0 {
                b.reply(send, adminID, "Получателей нет — рассылка не требуется.")
                return
        }

        go func() {
                var sent, failed int
                for _, rid := range recipients {
                        if err := send(rid, draft.text, telegramapi.SendOptions{}); err != nil {
                                failed++
                                log.Printf("[broadcast] получателю %d: %v", rid, err)
                        } else {
                                sent++
                        }
                        time.Sleep(b.cfg.BroadcastDelay)
                }
                b.reply(send, adminID, fmt.Sprintf("Рассылка завершена: доставлено %d, ошибок %d (из %d).", sent, failed, len(recipients)))
        }()

        b.reply(send, adminID, fmt.Sprintf("Отправляю %d сообщениям — займёт примерно %s. Результат пришлю сюда.",
                len(recipients), (time.Duration(len(recipients))*b.cfg.BroadcastDelay).Round(time.Second)))
}

// ---------- хелперы ----------

func (b *Bot) reply(send Sender, id int64, text string) {
        if err := send(id, text, telegramapi.SendOptions{}); err != nil {
                log.Printf("[bot] отправка %d: %v", id, err)
        }
}

func (b *Bot) replyWithKeyboard(send Sender, id int64, text string, rows [][]string) {
        opts := telegramapi.SendOptions{InlineKeyboard: rows}
        if err := send(id, text, opts); err != nil {
                log.Printf("[bot] отправка %d: %v", id, err)
        }
}

// parseAnswer разбирает текстовый ответ: 1/2/3, А/a/Б/b/В/v, «пропустить».
func parseAnswer(text string) (int, bool) {
        t := strings.ToLower(strings.TrimSpace(text))
        switch t {
        case "пропустить", "skip", "ничего из этого", "-":
                return -1, true
        }
        switch t {
        case "1", "а", "a":
                return 0, true
        case "2", "б", "b":
                return 1, true
        case "3", "в", "v":
                return 2, true
        }
        return 0, false
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

func sortStrings(ss []string) { sort.Strings(ss) }

func helpText(isAdmin bool) string {
        text := "Я провожу короткий тест про вечерний отдых.\n\n" +
                "/start — приветствие и запуск теста\n/test — пройти тест заново\n/mystats — мой сохранённый результат\n/forget — удалить мои данные\n\n" +
                "Ответы на вопросы можно выбирать кнопками или писать цифрами 1–3 (или А/Б/В)."
        if isAdmin {
                text += "\n\nАдмин-команды: /admin"
        }
        return text
}

const adminHelpText = "Админ-команды:\n/admin — эта справка\n/stats — статистика базы\n/users — список профилей\n/broadcast — мастер массовой рассылки\n/send <ID> <текст> — личное сообщение\n/cancel — отменить текущую рассылку"
