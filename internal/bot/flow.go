package bot

// Сценарий теста: приветствие, вопросы, результат, практика.

import (
	"fmt"
	"strings"

	"github.com/restquiz/rest-test-bot/internal/quiz"
	"github.com/restquiz/rest-test-bot/internal/telegramapi"
)

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
