package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds application configuration loaded from environment variables.
type Config struct {
	DatabasePath     string        // path to sqlite file
	TelegramBotToken string        // telegram bot token
	TelegramChatID   int64         // target chat id
	PollInterval     time.Duration // interval between parser runs
	OlxSearchURLs    []string      // OLX search URLs to watch (comma-separated)
	RequestTimeout   time.Duration // HTTP request timeout
	UserAgent        string        // HTTP user-agent
	RequestDelay     time.Duration // base delay between requests
	RequestJitter    time.Duration // additional random jitter (0..jitter)
	HTTPAddr         string        // http listen address for admin form
	HTTPUser         string        // optional basic-auth user for the admin form
	HTTPPassword     string        // optional basic-auth password for the admin form
	MaxItemsPerRun   int           // max new listings reported per search per run

	SeedPairs   [][2]string // optional seed pairs (name,url)
	NotifyTimes []string    // times of day to send notifications (HH:MM format)
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load reads env vars and returns Config. Terminates the program if required
// variables are missing or malformed.
func Load() Config {
	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		log.Printf("Warning: .env file not found or error loading: %v", err)
	}

	chatIDStr := getEnv("TELEGRAM_CHAT_ID", "")
	var chatID int64
	if chatIDStr != "" {
		parsed, err := strconv.ParseInt(chatIDStr, 10, 64)
		if err != nil {
			log.Fatalf("invalid TELEGRAM_CHAT_ID: %v", err)
		}
		chatID = parsed
	}

	pollSecStr := getEnv("POLL_INTERVAL_SEC", "60")
	pollSec, err := strconv.Atoi(pollSecStr)
	if err != nil || pollSec <= 0 {
		log.Fatalf("invalid POLL_INTERVAL_SEC: %v", pollSecStr)
	}

	reqTimeoutStr := getEnv("REQUEST_TIMEOUT_SEC", "15")
	reqTimeoutSec, err := strconv.Atoi(reqTimeoutStr)
	if err != nil || reqTimeoutSec <= 0 {
		log.Fatalf("invalid REQUEST_TIMEOUT_SEC: %v", reqTimeoutStr)
	}

	// Parse multi-URL env: comma or whitespace separated
	urlsRaw := getEnv("OLX_SEARCH_URLS", "")
	var urls []string
	for _, part := range strings.FieldsFunc(urlsRaw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' ' }) {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			urls = append(urls, trimmed)
		}
	}

	// Parse optional seed pairs NAME|URL, separated by newlines
	var seedPairs [][2]string
	seedsRaw := getEnv("OLX_SEARCH_URLS_WITH_NAMES", "")
	if seedsRaw != "" {
		lines := strings.Split(seedsRaw, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "|", 2)
			if len(parts) == 2 {
				name := strings.TrimSpace(parts[0])
				url := strings.TrimSpace(parts[1])
				if url != "" {
					seedPairs = append(seedPairs, [2]string{name, url})
				}
			}
		}
	}

	maxItemsStr := getEnv("MAX_ITEMS_PER_RUN", "10")
	maxItems, err := strconv.Atoi(maxItemsStr)
	if err != nil || maxItems <= 0 {
		log.Fatalf("invalid MAX_ITEMS_PER_RUN: %v", maxItemsStr)
	}

	delaySec, _ := strconv.Atoi(getEnv("REQUEST_DELAY_SEC", "3"))
	jitterSec, _ := strconv.Atoi(getEnv("REQUEST_JITTER_SEC", "2"))

	// Parse daily notify times like "10:00,11:00,22:00"
	var notifyTimes []string
	timesRaw := getEnv("DAILY_NOTIFY_TIMES", "11:00,15:00,20:00")
	for _, t := range strings.Split(timesRaw, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		// Validate HH:MM format
		if len(t) == 5 && t[2] == ':' {
			if _, err := time.Parse("15:04", t); err == nil {
				notifyTimes = append(notifyTimes, t)
			}
		}
	}

	cfg := Config{
		DatabasePath:     getEnv("DATABASE_PATH", "./data/info-parser.db"),
		TelegramBotToken: getEnv("TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:   chatID,
		PollInterval:     time.Duration(pollSec) * time.Second,
		OlxSearchURLs:    urls,
		RequestTimeout:   time.Duration(reqTimeoutSec) * time.Second,
		UserAgent:        getEnv("HTTP_USER_AGENT", "Mozilla/5.0 (compatible; InfoParserBot/1.0; +https://example.com)"),
		RequestDelay:     time.Duration(delaySec) * time.Second,
		RequestJitter:    time.Duration(jitterSec) * time.Second,
		HTTPAddr:         getEnv("HTTP_ADDR", ":8088"),
		HTTPUser:         getEnv("HTTP_BASIC_AUTH_USER", ""),
		HTTPPassword:     getEnv("HTTP_BASIC_AUTH_PASS", ""),
		MaxItemsPerRun:   maxItems,
		SeedPairs:        seedPairs,
		NotifyTimes:      notifyTimes,
	}

	if cfg.TelegramBotToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is required")
	}
	if cfg.TelegramChatID == 0 {
		log.Fatal("TELEGRAM_CHAT_ID is required")
	}
	if (cfg.HTTPUser == "") != (cfg.HTTPPassword == "") {
		log.Fatal("HTTP_BASIC_AUTH_USER and HTTP_BASIC_AUTH_PASS must be set together")
	}
	if cfg.HTTPUser == "" {
		log.Printf("Warning: admin UI on %s has no authentication (set HTTP_BASIC_AUTH_USER/HTTP_BASIC_AUTH_PASS)", cfg.HTTPAddr)
	}
	return cfg
}
