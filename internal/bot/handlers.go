package bot

// Роутинг: текст, inline-кнопки, контакт.

import (
	"strconv"
	"strings"
	"time"

	"github.com/pilligrim28/rest-test-bot/internal/telegramapi"
)

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
			b.reply(send, u.SenderID, adminHelpText())
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
