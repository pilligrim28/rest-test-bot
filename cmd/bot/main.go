// Command bot — точка входа Telegram-бота «Что не даёт вам нормально отдыхать вечером?».
//
// Запуск: go run cmd/bot/main.go (читает .env из корня проекта автоматически).
package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pilligrim28/rest-test-bot/internal/bot"
	"github.com/pilligrim28/rest-test-bot/internal/config"
	"github.com/pilligrim28/rest-test-bot/internal/quiz"
	"github.com/pilligrim28/rest-test-bot/internal/store"
	"github.com/pilligrim28/rest-test-bot/internal/telegramapi"
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

	// Автоудаление профилей по истечении срока хранения (ст. 5 152-ФЗ).
	purge := func() {
		if n, err := st.Purge(cfg.Retention); err != nil {
			log.Printf("[restquiz] автоудаление: %v", err)
		} else if n > 0 {
			log.Printf("[restquiz] удалено профилей с истёкшим сроком хранения: %d", n)
		}
	}
	purge()
	go func() {
		for range time.Tick(24 * time.Hour) {
			purge()
		}
	}()

	b := bot.New(cfg, st, api)
	send := api.Send

	updates := make(chan telegramapi.Update, 100)
	stop := make(chan struct{})

	go func() {
		for u := range updates {
			b.HandleUpdate(u, send)
		}
	}()

	if cfg.WebhookURL != "" {
		// Режим вебхука: HTTPS терминирует обратный прокси (nginx/Caddy),
		// бот слушает HTTP на WEBHOOK_LISTEN.
		if err := api.SetWebhookWithSecret(cfg.WebhookURL, cfg.WebhookSecret); err != nil {
			log.Fatalf("[restquiz] setWebhook: %v", err)
		}
		go func() {
			if err := telegramapi.ServeWebhook(cfg.WebhookListen, cfg.WebhookPath, cfg.WebhookSecret, updates, stop); err != nil {
				log.Fatalf("[restquiz] сервер вебхука: %v", err)
			}
		}()
		log.Printf("[restquiz] режим webhook: %s, слушаю %s%s", cfg.WebhookURL, cfg.WebhookListen, cfg.WebhookPath)
		sig := waitSignal()
		log.Printf("[restquiz] получен сигнал %s, останавливаюсь…", sig)
		close(stop)
		return
	}

	// Long polling: сначала гарантированно удаляем активный вебхук,
	// иначе Telegram отвечает «Conflict: can't use getUpdates method while webhook is active».
	if err := api.EnsureNoWebhook(); err != nil {
		log.Printf("[restquiz] deleteWebhook: %v (продолжаю, poller починит конфликт сам)", err)
	}
	go api.PollUpdates(updates, stop)

	log.Printf("[restquiz] бот запущен как @%s (long polling), админов: %d, профилей: %d",
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
