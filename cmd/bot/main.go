// Command bot — точка входа Telegram-бота «Что не даёт вам нормально отдыхать вечером?».
//
// Запуск: go run cmd/bot/main.go (читает .env из корня проекта автоматически).
package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/restquiz/rest-test-bot/internal/bot"
	"github.com/restquiz/rest-test-bot/internal/config"
	"github.com/restquiz/rest-test-bot/internal/quiz"
	"github.com/restquiz/rest-test-bot/internal/store"
	"github.com/restquiz/rest-test-bot/internal/telegramapi"
)

func main() {
	log.SetFlags(log.LstdFlags)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[restquiz] конфигурация: %v", err)
	}

	// Ссылка на практику берётся из PRACTICE_URL (.env), иначе — значение по умолчанию.
	if cfg.PracticeURL != "" {
		quiz.PracticeURL = cfg.PracticeURL
	}

	api, err := telegramapi.New(cfg.Token)
	if err != nil {
		log.Fatalf("[restquiz] %v", err)
	}

	st, err := store.New(cfg.DataFile)
	if err != nil {
		log.Fatalf("[restquiz] хранилище: %v", err)
	}

	b := bot.New(cfg, st, api)
	send := api.Send

	updates := make(chan telegramapi.Update)
	stop := make(chan struct{})

	if cfg.WebhookURL != "" {
		// Режим вебхука: URL должен указывать на HTTPS-эндпоинт бэкенда.
		if err := api.SetWebhook(cfg.WebhookURL); err != nil {
			log.Fatalf("[restquiz] setWebhook: %v", err)
		}
		log.Printf("[restquiz] вебхук установлен: %s (для локального тестирования уберите WEBHOOK_URL)", cfg.WebhookURL)
		log.Printf("[restquiz] режим webhook: обновления принимает HTTP-сервер, запуск long polling отменён")
		waitSignal()
		return
	}

	// Long polling: сначала гарантированно удаляем активный вебхук,
	// иначе Telegram отвечает «Conflict: can't use getUpdates method while webhook is active».
	if err := api.EnsureNoWebhook(); err != nil {
		log.Printf("[restquiz] deleteWebhook: %v (продолжаю, poller починит конфликт сам)", err)
	}

	go api.PollUpdates(updates, stop)

	go func() {
		for u := range updates {
			b.HandleUpdate(u, send)
		}
	}()

	log.Printf("[restquiz] бот запущен как @%s, админов: %d, профилей: %d",
		api.BotUsername(), cfg.AdminCount(), st.Count())

	sig := waitSignal()
	log.Printf("[restquiz] получен сигнал %s, останавливаюсь…", sig)
	close(stop)

	// Даём poller'у завершиться.
	done := make(chan struct{})
	go func() {
		for range updates {
		}
		close(done)
	}()
	<-done
	log.Printf("[restquiz] остановлен")
}

func waitSignal() os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	return <-ch
}
