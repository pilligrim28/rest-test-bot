package bot

// Подписка и согласие на обработку ПДн, права субъекта.

import (
	"fmt"
	"log"
	"strings"

	"github.com/pilligrim28/rest-test-bot/internal/quiz"
	"github.com/pilligrim28/rest-test-bot/internal/store"
	"github.com/pilligrim28/rest-test-bot/internal/telegramapi"
)

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
