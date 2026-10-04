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
	"log"
	"runtime/debug"
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
