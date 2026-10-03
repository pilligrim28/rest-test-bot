package config

import (
	"testing"
)

func TestWebhookURLCleared(t *testing.T) {
	t.Setenv(envToken, "123:ABC")
	t.Setenv(envWebhook, "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebhookURL != "" {
		t.Fatalf("ожидался пустой WebhookURL, получили %q", cfg.WebhookURL)
	}
}

func TestWebhookURLRead(t *testing.T) {
	t.Setenv(envToken, "123:ABC")
	t.Setenv(envWebhook, "https://example.com/hook")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebhookURL != "https://example.com/hook" {
		t.Fatalf("WebhookURL = %q", cfg.WebhookURL)
	}
}
