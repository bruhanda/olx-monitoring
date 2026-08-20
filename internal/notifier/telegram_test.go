package notifier

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestTelegram(t *testing.T, h http.HandlerFunc) *Telegram {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	tg := NewTelegram("token", 42, 5*time.Second)
	tg.apiURL = srv.URL
	return tg
}

func TestSendMessagePayload(t *testing.T) {
	var payload map[string]any
	tg := newTestTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/sendMessage" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &payload)
		w.Write([]byte(`{"ok":true}`))
	})

	if err := tg.SendMessage("<b>hi</b>"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if payload["text"] != "<b>hi</b>" || payload["parse_mode"] != "HTML" || payload["chat_id"].(float64) != 42 {
		t.Fatalf("unexpected payload: %v", payload)
	}
}

func TestSendMessageRetriesOnRateLimit(t *testing.T) {
	var calls int32
	tg := newTestTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"ok":false,"parameters":{"retry_after":1}}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})

	if err := tg.SendMessage("hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestSendMessageReportsAPIError(t *testing.T) {
	tg := newTestTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok":false,"description":"can't parse entities"}`))
	})

	err := tg.SendMessage("hi")
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "parse entities") {
		t.Fatalf("error = %v, want status and description", err)
	}
}
