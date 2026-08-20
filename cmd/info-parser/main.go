package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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

	// Start HTTP server
	httpServer := httpserver.New(store, httpserver.Options{
		NotifyTimes: cfg.NotifyTimes,
		Username:    cfg.HTTPUser,
		Password:    cfg.HTTPPassword,
	})
	go func() {
		if err := httpServer.Start(cfg.HTTPAddr); err != nil {
			log.Printf("HTTP server error: %v", err)
		}
	}()

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
		AtTimes(cfg.NotifyTimes)

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
