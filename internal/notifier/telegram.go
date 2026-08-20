package notifier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Telegram struct {
	client *http.Client
	token  string
	chatID int64
	apiURL string // overridable for tests
}

func NewTelegram(token string, chatID int64, timeout time.Duration) *Telegram {
	return &Telegram{
		client: &http.Client{Timeout: timeout},
		token:  token,
		chatID: chatID,
		apiURL: "https://api.telegram.org",
	}
}

// SendMessage posts a HTML-formatted message, retrying once when Telegram
// rate-limits the bot.
func (t *Telegram) SendMessage(text string) error {
	err := t.send(text)
	if rl, ok := err.(rateLimitError); ok {
		time.Sleep(rl.retryAfter)
		return t.send(text)
	}
	return err
}

type rateLimitError struct {
	retryAfter time.Duration
}

func (e rateLimitError) Error() string {
	return fmt.Sprintf("telegram rate limited, retry after %s", e.retryAfter)
}

func (t *Telegram) send(text string) error {
	url := fmt.Sprintf("%s/bot%s/sendMessage", t.apiURL, t.token)
	payload := map[string]any{
		"chat_id":                  t.chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		io.Copy(io.Discard, resp.Body)
		return nil
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode == http.StatusTooManyRequests {
		return rateLimitError{retryAfter: retryAfter(resp, respBody)}
	}
	return fmt.Errorf("telegram sendMessage failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
}

// maxRetryAfter caps how long a single send may wait before retrying.
const maxRetryAfter = 30 * time.Second

// retryAfter reads Telegram's suggested backoff, defaulting to a second.
func retryAfter(resp *http.Response, body []byte) time.Duration {
	var parsed struct {
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Parameters.RetryAfter > 0 {
		return min(time.Duration(parsed.Parameters.RetryAfter)*time.Second, maxRetryAfter)
	}
	if v := resp.Header.Get("Retry-After"); v != "" {
		if d, err := time.ParseDuration(v + "s"); err == nil && d > 0 {
			return min(d, maxRetryAfter)
		}
	}
	return time.Second
}
