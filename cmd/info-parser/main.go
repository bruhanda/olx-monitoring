package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	cfgpkg "github.com/bruhanda/olx-monitoring/internal/config"
	"github.com/bruhanda/olx-monitoring/internal/httpserver"
	"github.com/bruhanda/olx-monitoring/internal/notifier"
	olxparser "github.com/bruhanda/olx-monitoring/internal/parser/olx"
	"github.com/bruhanda/olx-monitoring/internal/scheduler"
	"github.com/bruhanda/olx-monitoring/internal/storage"
)

const sourceOLX = "olx"

func main() {
	cfg := cfgpkg.Load()

	if dir := filepath.Dir(cfg.DatabasePath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("failed to ensure data dir %s: %v", dir, err)
		}
	}

	store := storage.Open(cfg.DatabasePath)
	tg := notifier.NewTelegram(cfg.TelegramBotToken, cfg.TelegramChatID, cfg.RequestTimeout)

	// Seed searches from env if provided
	for _, pr := range cfg.SeedPairs {
		if _, err := store.CreateSearchIfNotExists(pr[0], pr[1], sourceOLX, true); err != nil {
			log.Printf("seed error: %v", err)
		}
	}
	for _, u := range cfg.OlxSearchURLs {
		if _, err := store.CreateSearchIfNotExists(u, u, sourceOLX, true); err != nil {
			log.Printf("seed error: %v", err)
		}
	}

	// Розклад живе в базі, щоб його можна було міняти з веб-адмінки; env
	// лишається значенням за замовчуванням для першого запуску.
	if _, ok, err := store.GetSetting(storage.SettingNotifyTimes); err != nil {
		log.Printf("failed to read schedule setting: %v", err)
	} else if !ok {
		if err := store.SetSetting(storage.SettingNotifyTimes, strings.Join(cfg.NotifyTimes, ",")); err != nil {
			log.Printf("failed to seed schedule setting: %v", err)
		}
	}

	notifyTimes := func() []string {
		raw, ok, err := store.GetSetting(storage.SettingNotifyTimes)
		if err != nil {
			log.Printf("failed to read schedule: %v", err)
			return cfg.NotifyTimes
		}
		if !ok {
			return cfg.NotifyTimes
		}
		times, err := cfgpkg.ParseNotifyTimes(raw)
		if err != nil {
			log.Printf("stored schedule %q is invalid, falling back to env: %v", raw, err)
			return cfg.NotifyTimes
		}
		return times
	}

	httpClient := &http.Client{Timeout: cfg.RequestTimeout}

	// Jobs are rebuilt before every run, so changes made in the admin UI are
	// picked up without a restart.
	jobs := func() ([]scheduler.Job, error) {
		searches, err := store.ListActiveSearches(sourceOLX)
		if err != nil {
			return nil, err
		}
		list := make([]scheduler.Job, 0, len(searches))
		for _, s := range searches {
			list = append(list, scheduler.Job{
				Search: s,
				Parser: olxparser.NewWithName(httpClient, cfg.UserAgent, s.URL, s.Name),
			})
		}
		return list, nil
	}

	sched := scheduler.New(store, tg, cfg.PollInterval, jobs).
		WithDelays(cfg.RequestDelay, cfg.RequestJitter).
		WithLimit(cfg.MaxItemsPerRun).
		WithTimes(notifyTimes)

	// Start HTTP server
	httpServer := httpserver.New(store, httpserver.Options{
		NotifyTimes: notifyTimes,
		SaveNotifyTimes: func(times []string) error {
			if err := store.SetSetting(storage.SettingNotifyTimes, strings.Join(times, ",")); err != nil {
				return err
			}
			sched.Reload() // застосувати новий розклад без рестарту
			return nil
		},
		Username: cfg.HTTPUser,
		Password: cfg.HTTPPassword,
	})
	go func() {
		if err := httpServer.Start(cfg.HTTPAddr); err != nil {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	stop := make(chan struct{})
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		log.Printf("shutting down")
		close(stop)
	}()

	sched.Run(stop)
}
