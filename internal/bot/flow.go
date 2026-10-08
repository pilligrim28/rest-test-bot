package bot

// Сценарий теста: приветствие, вопросы, результат, практика.

import (
	"fmt"
	"log"
	"strings"

	"github.com/pilligrim28/rest-test-bot/internal/quiz"
	"github.com/pilligrim28/rest-test-bot/internal/telegramapi"
)

const welcomeText = "*Тест: «Почему ты не можешь нормально отдохнуть вечером?»*\n\n" +
	"Рабочий день закончился. А голова всё ещё решает задачи.\n\n" +
	"Ты берёшь телефон на минуту — проходит час. Добираешься до отдыха — и сил хватает только упасть на диван. Вечер прошёл. А ты его даже не заметила.\n\n" +
	"Знакомо? Тогда этот тест для тебя.\n\n" +
	"Ответь на *5 коротких вопросов* и узнай:\n" +
	"• Где именно ты застреваешь вечером: в рабочих мыслях, в бесконечном переключении или в привычке откладывать отдых.\n" +
	"• Что можно попробовать уже сегодня, чтобы вечер не прошёл мимо тебя. Без «просто расслабься».\n" +
	"• И забери бесплатную аудиопрактику «Переключатель» — 8 минут с голосовым сопровождением, которая поможет переключиться прямо сейчас.\n\n" +
	"Выбирай ответы, которые ближе к твоим обычным вечерам.\n\n" +
	"_Это тест для самонаблюдения, а не диагностика. Но он может показать то, что ты давно не замечала._"

// practiceText — последний экран: текст над аудиопрактикой.
const practiceText = "🎧 «Переключатель: 8 минут, чтобы перейти от дел к отдыху»\n\n" +
	"Ты уже знаешь, где застреваешь вечером. Теперь попробуй то, что поможет переключиться уже сегодня.\n\n" +
	"Что понадобится:\n" +
	"— удобное место, где можно сесть;\n" +
	"— лист бумаги;\n" +
	"— 8 минут тишины для себя.\n\n" +
	"Как это будет:\n" +
	"Сядь удобно. Включи запись. Я проведу тебя шаг за шагом."

func (b *Bot) sendWelcome(id int64, send Sender) {
	opts := telegramapi.SendOptions{
		Markdown: true,
		Photo:    b.cfg.WelcomePhoto,
		InlineKeyboard: [][]string{
			{"Узнать, что мешает мне отдыхать", callbackStart},
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
	rows = append(rows, answerRow)

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
		for _, par := range cp.Body {
			sb.WriteString(par + "\n\n")
		}
	}

	photo := b.cfg.ResultTiePhoto
	button := "Попробовать восьмиминутную практику"
	footer := "Бесплатно · 8 минут · Без специальной подготовки (нужен только лист бумаги)"
	if outcome.IsTie() {
		sb.WriteString("🔎 *Твой результат*\n\n*" + quiz.TieCopy.Title + "*\n\n")
		for _, line := range quiz.TieCopy.Lead {
			sb.WriteString(line + "\n\n")
		}
		for _, c := range outcome.Categories {
			writeCopy(quiz.Results[c])
		}
	} else {
		cat := outcome.Categories[0]
		photo = b.cfg.ResultPhoto(categoryIndex(cat) + 1)
		cp := quiz.Results[cat]
		sb.WriteString("🔎 *Твой результат*\n\n")
		writeCopy(cp)
		button, footer = cp.ButtonLabel, cp.Footer
	}
	sb.WriteString("_" + footer + "_")

	b.send(send, id, sb.String(), telegramapi.SendOptions{
		Markdown:       true,
		Photo:          photo,
		InlineKeyboard: [][]string{{"🎧 " + button, callbackPractice}},
	})
}

// deliverPractice присылает практику прямо в чат. Для этого достаточно
// Telegram ID текущего диалога — никакие данные не сохраняются.
func (b *Bot) deliverPractice(id int64, st *state, send Sender) {
	if b.cfg.PracticeAudio != "" {
		// Telegram показывает подпись только под аудио, поэтому текст идёт
		// отдельным сообщением перед файлом — так он оказывается над плеером.
		b.reply(send, id, practiceText)
		b.send(send, id, "", telegramapi.SendOptions{
			Audio:      b.cfg.PracticeAudio,
			AudioTitle: "Переключатель: 8 минут, чтобы перейти от дел к отдыху",
		})
	} else {
		b.reply(send, id, practiceText+"\n\n"+quiz.PracticeURL)
	}

	// Уже подписан — согласие повторно не спрашиваем, только обновляем результат.
	if p, ok := b.store.Get(id); ok && p.Subscribed() {
		if st != nil && st.headline != "" {
			if _, err := b.store.UpdateResult(id, st.scores, st.headline); err != nil {
				log.Printf("[bot] обновление результата %d: %v", id, err)
			}
		}
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
			{"🔒 Как я обращаюсь с данными", callbackConsentInfo},
		}})
}

// categoryIndex — номер варианта ответа (0..2), соответствующего категории.
func categoryIndex(c quiz.Category) int {
	for i, cat := range quiz.AllCategories {
		if cat == c {
			return i
		}
	}
	return -1
}
