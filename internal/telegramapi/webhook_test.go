package telegramapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

func signBotGate(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

func TestBotGateWebhookSignature(t *testing.T) {
	ch := make(chan Update, 1)
	h := BotGateWebhookHandler("gate", ch)

	bad := httptest.NewRequest(http.MethodPost, "/tg", strings.NewReader(sampleUpdate))
	bad.Header.Set(botGateSignatureHeader, signBotGate("other", sampleUpdate))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bad)
	if rec.Code != http.StatusForbidden || len(ch) != 0 {
		t.Fatalf("неверная подпись: ожидал 403, получил %d", rec.Code)
	}

	ok := httptest.NewRequest(http.MethodPost, "/tg", strings.NewReader(sampleUpdate))
	ok.Header.Set(botGateSignatureHeader, signBotGate("gate", sampleUpdate))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, ok)
	if rec.Code != http.StatusOK {
		t.Fatalf("верная подпись: код %d", rec.Code)
	}
	if u := <-ch; u.SenderID != 42 {
		t.Fatalf("неверное обновление: %+v", u)
	}
}

func TestBotGateClientUsesProxy(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Bot","username":"rest_bot"}}`))
	}))
	defer srv.Close()

	api, err := NewBotGate(srv.URL+"/api/v1/bots/", "bot_abc", "bg_live_test")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/bots/bot_abc/getMe" || gotAuth != "Bearer bg_live_test" {
		t.Fatalf("запрос ушёл не туда: path=%q auth=%q", gotPath, gotAuth)
	}
	if api.BotUsername() != "rest_bot" {
		t.Fatalf("username %q", api.BotUsername())
	}
}
