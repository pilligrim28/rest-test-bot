package telegramapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// secretHeader — заголовок, которым Telegram подписывает запросы вебхука.
const secretHeader = "X-Telegram-Bot-Api-Secret-Token"

// SetWebhookWithSecret регистрирует вебхук. secret передаётся Telegram и
// затем приходит в каждом запросе — так чужие запросы отсекаются.
func (a *API) SetWebhookWithSecret(rawURL, secret string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return fmt.Errorf("WEBHOOK_URL должен быть https-адресом: %q", rawURL)
	}
	params := tgbotapi.Params{"url": u.String()}
	if secret != "" {
		params["secret_token"] = secret
	}
	_, err = a.bot.MakeRequest("setWebhook", params)
	return err
}

// WebhookHandler возвращает HTTP-обработчик, который принимает обновления
// от Telegram и кладёт их в канал updates.
func WebhookHandler(secret string, updates chan<- Update) http.Handler {
	return webhookHandler(func(r *http.Request, _ []byte) bool {
		return secret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get(secretHeader)), []byte(secret)) == 1
	}, updates)
}

// webhookHandler — общий обработчик: authorized проверяет подпись запроса.
func webhookHandler(authorized func(r *http.Request, body []byte) bool, updates chan<- Update) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !authorized(r, body) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var upd tgbotapi.Update
		if err := json.Unmarshal(body, &upd); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if parsed, ok := toUpdate(upd); ok {
			select {
			case updates <- parsed:
			case <-time.After(5 * time.Second):
				log.Printf("[telegram] очередь обновлений переполнена, пропускаю %d", upd.UpdateID)
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

// ServeWebhook запускает HTTP-сервер вебхука до закрытия stop.
func ServeWebhook(listen, path, secret string, updates chan<- Update, stop <-chan struct{}) error {
	return Serve(listen, path, WebhookHandler(secret, updates), stop)
}

// Serve запускает HTTP-сервер с обработчиком обновлений handler до закрытия stop.
func Serve(listen, path string, handler http.Handler, stop <-chan struct{}) error {
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
