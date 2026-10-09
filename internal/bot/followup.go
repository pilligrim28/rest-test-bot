package bot

// Сценарий после выдачи практики:
//
//	практика → через 1 ч «Получилось прослушать?» [Да] [Нет]
//	  «Нет» → напоминание через 6 ч и через 24 ч (от момента ответа «Нет»)
//	  «Да»  → через 1 ч предложение подписки: [✅ Согласен] [📄 Пользовательское соглашение] [Нет, спасибо]
//	           «Согласен» → согласие на рассылку и обработку данных → номер телефона.
//
// Этапы хранятся в файле данных (store.Followup), поэтому переживают перезапуск.

import (
	"log"
	"time"

	"github.com/pilligrim28/rest-test-bot/internal/store"
	"github.com/pilligrim28/rest-test-bot/internal/telegramapi"
)

// Задержки сценария; в тестах подменяются.
var (
	listenCheckDelay = time.Hour
	remindDelays     = []time.Duration{6 * time.Hour, 24 * time.Hour}
	offerDelay       = time.Hour
	schedulerTick    = 30 * time.Second
)

const (
	fuAskListen = "ask_listen" // ждём часа, чтобы спросить «Получилось?»
	fuAsked     = "asked"      // спросили, ждём ответа
	fuRemind    = "remind"     // после «Нет»: ждём следующего напоминания
	fuOffer     = "offer"      // после «Да»: ждём часа, чтобы предложить подписку
	fuOffered   = "offered"    // предложили подписку, ждём ответа

	callbackListenYes = "listen_yes"
	callbackListenNo  = "listen_no"
	callbackAgree     = "agree_all"
	callbackAgreement = "agreement"
	callbackDecline   = "decline_all"
)

const listenQuestion = "Получилось прослушать или нет?"

// scheduleListenCheck ставит вопрос «Получилось прослушать?» через час после практики.
func (b *Bot) scheduleListenCheck(id int64, st *state) {
	f := store.Followup{ChatID: id, Stage: fuAskListen, DueAt: time.Now().UTC().Add(listenCheckDelay)}
	if st != nil {
		f.Headline, f.Scores = st.headline, st.scores
	}
	if err := b.store.SetFollowup(f); err != nil {
		log.Printf("[followup] %d: %v", id, err)
	}
}

// RunScheduler раз в schedulerTick отправляет сообщения, время которых подошло.
func (b *Bot) RunScheduler(send Sender, stop <-chan struct{}) {
	t := time.NewTicker(schedulerTick)
	defer t.Stop()
	for {
		b.ProcessDue(time.Now().UTC(), send)
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// ProcessDue отправляет все отложенные сообщения, время которых наступило к now.
func (b *Bot) ProcessDue(now time.Time, send Sender) {
	for _, f := range b.store.DueFollowups(now) {
		b.fireFollowup(f, now, send)
	}
}

func (b *Bot) listenButtons() [][]string {
	return [][]string{{"Да", callbackListenYes, "Нет", callbackListenNo}}
}

func (b *Bot) fireFollowup(f store.Followup, now time.Time, send Sender) {
	switch f.Stage {
	case fuAskListen:
		b.send(send, f.ChatID, listenQuestion, telegramapi.SendOptions{InlineKeyboard: b.listenButtons()})
		f.Stage, f.DueAt = fuAsked, time.Time{}
	case fuRemind:
		b.send(send, f.ChatID, "Напоминаю про практику «Переключатель» — всего 8 минут, чтобы перейти от дел к отдыху. Запись выше в чате.\n\n"+listenQuestion,
			telegramapi.SendOptions{InlineKeyboard: b.listenButtons()})
		f.DueAt = time.Time{}
		f.Stage = fuAsked
		if next, ok := nextReminder(f.NoAt, now); ok {
			f.Stage, f.DueAt = fuRemind, next
		}
	case fuOffer:
		b.sendOffer(f.ChatID, send)
		f.Stage, f.DueAt = fuOffered, time.Time{}
	default:
		f.DueAt = time.Time{}
	}
	if err := b.store.SetFollowup(f); err != nil {
		log.Printf("[followup] %d: %v", f.ChatID, err)
	}
}

// nextReminder — ближайшее ещё не наступившее напоминание после ответа «Нет».
func nextReminder(noAt, now time.Time) (time.Time, bool) {
	for _, d := range remindDelays {
		if at := noAt.Add(d); at.After(now) {
			return at, true
		}
	}
	return time.Time{}, false
}

func (b *Bot) onListenNo(id int64, send Sender) {
	f, ok := b.store.GetFollowup(id)
	if !ok {
		f = store.Followup{ChatID: id}
	}
	now := time.Now().UTC()
	// Напоминания считаются от первого «Нет»; повторное «Нет» их не сдвигает.
	if f.NoAt.IsZero() {
		f.NoAt = now
	}
	next, more := nextReminder(f.NoAt, now)
	if !more {
		if err := b.store.DeleteFollowup(id); err != nil {
			log.Printf("[followup] %d: %v", id, err)
		}
		b.reply(send, id, "Хорошо, больше не напоминаю. Практика остаётся выше в чате — возвращайся к ней, когда будет 8 минут для себя.")
		return
	}
	f.Stage, f.DueAt = fuRemind, next
	if err := b.store.SetFollowup(f); err != nil {
		log.Printf("[followup] %d: %v", id, err)
	}
	b.reply(send, id, "Ничего страшного. Я напомню чуть позже — практика никуда не денется, она выше в чате.")
}

func (b *Bot) onListenYes(id int64, send Sender) {
	b.reply(send, id, "Здорово! Спасибо, что нашла 8 минут для себя 💛\n\nПовторяй это, когда заметишь, что снова застреваешь вечером.")
	if p, ok := b.store.Get(id); ok && p.Subscribed() {
		if err := b.store.DeleteFollowup(id); err != nil {
			log.Printf("[followup] %d: %v", id, err)
		}
		return
	}
	f, ok := b.store.GetFollowup(id)
	if !ok {
		f = store.Followup{ChatID: id}
	}
	f.Stage, f.DueAt = fuOffer, time.Now().UTC().Add(offerDelay)
	if err := b.store.SetFollowup(f); err != nil {
		log.Printf("[followup] %d: %v", id, err)
	}
}

func offerButtons() [][]string {
	return [][]string{
		{"✅ Согласен", callbackAgree},
		{"📄 Пользовательское соглашение", callbackAgreement},
		{"Нет, спасибо", callbackDecline},
	}
}

func (b *Bot) sendOffer(id int64, send Sender) {
	b.send(send, id,
		"Хочешь получать новые короткие практики и материалы про вечерний отдых? Буду присылать их сюда, в Telegram.\n\n"+
			"Для этого нужно твоё согласие на рассылку и на обработку данных: логин Telegram и номер телефона. "+
			"Подробности — в пользовательском соглашении. Отписаться и удалить данные можно в любой момент командой /forget.",
		telegramapi.SendOptions{InlineKeyboard: offerButtons()})
}

// showAgreement показывает соглашение только по нажатию кнопки, в конце — те же кнопки.
func (b *Bot) showAgreement(id int64, send Sender) {
	b.showConsentFull(id, send)
	b.send(send, id, "Если согласна — нажми «Согласен».", telegramapi.SendOptions{InlineKeyboard: [][]string{
		{"✅ Согласен", callbackAgree},
		{"Нет, спасибо", callbackDecline},
	}})
}

// onAgree — одно нажатие «Согласен»: согласие на рассылку и на обработку данных,
// затем просим номер телефона (логин Telegram берётся из профиля автоматически).
func (b *Bot) onAgree(u telegramapi.Update, send Sender) {
	id := u.SenderID
	st := b.getState(id)
	if st == nil {
		st = &state{}
		b.setState(id, st)
	}
	if f, ok := b.store.GetFollowup(id); ok {
		if st.headline == "" {
			st.headline, st.scores = f.Headline, f.Scores
		}
		if err := b.store.DeleteFollowup(id); err != nil {
			log.Printf("[followup] %d: %v", id, err)
		}
	}
	now := time.Now().UTC()
	st.marketingAt, st.consentAt = now, now
	if b.cfg.CollectPhone {
		st.stage = stagePhone
		b.askPhone(id, send, "Спасибо! Поделись, пожалуйста, номером телефона — кнопкой ниже, вводить вручную не нужно.")
		return
	}
	b.saveProfile(u, st, "", send)
}

func (b *Bot) onDecline(id int64, send Sender) {
	b.clearState(id)
	if err := b.store.DeleteFollowup(id); err != nil {
		log.Printf("[followup] %d: %v", id, err)
	}
	b.reply(send, id, "Хорошо, ничего не сохраняю. Практика остаётся у тебя. Передумаешь — /start.")
}
