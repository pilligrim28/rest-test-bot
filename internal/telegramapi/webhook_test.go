package telegramapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sampleUpdate = `{"update_id":1,"message":{"message_id":1,"from":{"id":42,"first_name":"Аня","username":"anya"},"chat":{"id":42,"type":"private"},"date":0,"text":"/start"}}`

func TestWebhookRejectsWrongSecret(t *testing.T) {
	ch := make(chan Update, 1)
	h := WebhookHandler("s3cret", ch)
	req := httptest.NewRequest(http.MethodPost, "/tg", strings.NewReader(sampleUpdate))
	req.Header.Set(secretHeader, "wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || len(ch) != 0 {
		t.Fatalf("ожидал 403 без обновления, получил %d", rec.Code)
	}
}

func TestWebhookAcceptsUpdate(t *testing.T) {
	ch := make(chan Update, 1)
	h := WebhookHandler("s3cret", ch)
	req := httptest.NewRequest(http.MethodPost, "/tg", strings.NewReader(sampleUpdate))
	req.Header.Set(secretHeader, "s3cret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	u := <-ch
	if u.SenderID != 42 || u.Text != "/start" || u.Username != "anya" {
		t.Fatalf("неверное обновление: %+v", u)
	}
}

func TestGroupMessagesIgnored(t *testing.T) {
	ch := make(chan Update, 1)
	h := WebhookHandler("", ch)
	body := strings.Replace(sampleUpdate, `"type":"private"`, `"type":"group"`, 1)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/tg", strings.NewReader(body)))
	if len(ch) != 0 {
		t.Fatal("сообщения из групп обрабатываться не должны")
	}
}

func TestConflictKinds(t *testing.T) {
	other := errors.New("Conflict: terminated by other getUpdates request; make sure that only one bot instance is running")
	hook := errors.New("Conflict: can't use getUpdates method while webhook is active; use deleteWebhook to delete the webhook first")
	if !isOtherInstance(other) || isOtherInstance(hook) {
		t.Fatal("вторую копию бота надо отличать от активного вебхука")
	}
	if !isConflict(hook) {
		t.Fatal("активный вебхук — это конфликт")
	}
}
