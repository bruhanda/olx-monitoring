package scheduler

import (
	"fmt"
	"html"
	"log"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/bruhanda/olx-monitoring/internal/notifier"
	"github.com/bruhanda/olx-monitoring/internal/parser"
	"github.com/bruhanda/olx-monitoring/internal/storage"
)

// defaultLimit caps how many new listings of a single search are pushed to
// Telegram in one run.
const defaultLimit = 10

// Job pairs a saved search with the parser that fetches it.
type Job struct {
	Search storage.SavedSearch
	Parser parser.Parser
}

// JobProvider builds the jobs for a run. It is called before every run, so
// searches added, edited or disabled through the admin UI take effect without
// restarting the process.
type JobProvider func() ([]Job, error)

type Scheduler struct {
	store     *storage.Store
	notifier  *notifier.Telegram
	jobs      JobProvider
	interval  time.Duration
	baseDelay time.Duration
	jitter    time.Duration
	times     func() []string
	limit     int
	reload    chan struct{}
}

func New(store *storage.Store, notifier *notifier.Telegram, interval time.Duration, jobs JobProvider) *Scheduler {
	return &Scheduler{
		store:    store,
		notifier: notifier,
		jobs:     jobs,
		interval: interval,
		limit:    defaultLimit,
		times:    func() []string { return nil },
		reload:   make(chan struct{}, 1),
	}
}

func (s *Scheduler) WithDelays(baseDelay, jitter time.Duration) *Scheduler {
	s.baseDelay = baseDelay
	s.jitter = jitter
	return s
}

// WithLimit caps the number of new listings reported per search per run.
func (s *Scheduler) WithLimit(limit int) *Scheduler {
	if limit > 0 {
		s.limit = limit
	}
	return s
}

// WithTimes задає джерело розкладу. Провайдер викликається перед кожним
// очікуванням, тож зміни з веб-адмінки застосовуються без рестарту.
func (s *Scheduler) WithTimes(times func() []string) *Scheduler {
	if times != nil {
		s.times = times
	}
	return s
}

// AtTimes — зручна обгортка для сталого розкладу (тести, простий запуск).
func (s *Scheduler) AtTimes(times []string) *Scheduler {
	fixed := append([]string(nil), times...)
	sort.Strings(fixed)
	return s.WithTimes(func() []string { return fixed })
}

// Reload будить планувальник, щоб він перечитав розклад негайно.
// Безпечно викликати з будь-якої горутини; зайві сигнали відкидаються.
func (s *Scheduler) Reload() {
	select {
	case s.reload <- struct{}{}:
	default:
	}
}

func (s *Scheduler) Run(stop <-chan struct{}) {
	first := true
	for {
		times := s.times()

		var wait time.Duration
		switch {
		case len(times) == 0 && first:
			// Режим інтервалу: перший прогін одразу після старту.
			wait = 0
		case len(times) == 0:
			wait = s.interval
		default:
			next := nextOccurrence(time.Now(), times)
			wait = time.Until(next)
			log.Printf("scheduler: розклад %s, наступний запуск %s",
				strings.Join(times, ", "), next.Format("2006-01-02 15:04"))
		}
		first = false

		t := time.NewTimer(wait)
		select {
		case <-t.C:
			s.runOnce()
		case <-s.reload:
			if !t.Stop() {
				<-t.C
			}
			log.Printf("scheduler: розклад змінено, перераховую")
		case <-stop:
			if !t.Stop() {
				<-t.C
			}
			return
		}
	}
}

func nextOccurrence(now time.Time, times []string) time.Time {
	year, month, day := now.Date()
	loc := now.Location()
	// try today remaining times
	for _, t := range times {
		parsed, err := time.ParseInLocation("15:04", t, loc)
		if err != nil {
			continue
		}
		target := time.Date(year, month, day, parsed.Hour(), parsed.Minute(), 0, 0, loc)
		if target.After(now) || target.Equal(now) {
			return target
		}
	}
	// otherwise next day first time
	first, err := time.ParseInLocation("15:04", times[0], loc)
	if err != nil {
		return now.Add(24 * time.Hour)
	}
	return time.Date(year, month, day, first.Hour(), first.Minute(), 0, 0, loc).Add(24 * time.Hour)
}

// jobResult is the outcome of a single search within one run.
type jobResult struct {
	label string
	err   error
	new   int
}

func (s *Scheduler) runOnce() {
	jobs, err := s.jobs()
	if err != nil {
		log.Printf("scheduler: failed to load searches: %v", err)
		s.send(fmt.Sprintf("⚠️ Не вдалося завантажити список пошуків: %s", escape(err.Error())))
		return
	}
	if len(jobs) == 0 {
		log.Printf("scheduler: no active searches, nothing to do")
		return
	}

	results := make([]jobResult, 0, len(jobs))
	for i, job := range jobs {
		results = append(results, s.runJob(job))
		if i < len(jobs)-1 {
			s.sleepBetweenRequests()
		}
	}
	s.send(runSummary(time.Now(), results))
}

// runJob fetches one search, stores what it found and notifies about listings
// that were not seen before.
func (s *Scheduler) runJob(job Job) jobResult {
	p := job.Parser
	res := jobResult{label: p.Label()}

	items, err := p.Fetch()
	if err != nil {
		log.Printf("scheduler: [%s] fetch failed: %v", p.Label(), err)
		res.err = err
		return res
	}

	var fresh []parser.Item
	nonAd := 0
	for _, it := range items {
		if isAdItem(it) {
			continue
		}
		nonAd++

		created, err := s.store.CreateListingIfNotExists(&storage.Listing{
			SearchID:   job.Search.ID,
			Source:     p.Source(),
			ExternalID: it.ExternalID,
			Title:      it.Title,
			URL:        it.URL,
			Price:      it.Price,
			Location:   it.Location,
			PostedAt:   it.PostedAt,
		})
		if err != nil {
			log.Printf("scheduler: [%s] failed to store listing: %v", p.Label(), err)
			res.err = err
			continue
		}
		if created {
			fresh = append(fresh, it)
		}
	}

	res.new = len(fresh)
	log.Printf("scheduler: [%s] %d items, %d non-ad, %d new", p.Label(), len(items), nonAd, len(fresh))
	if len(fresh) == 0 {
		return res
	}

	shown := len(fresh)
	if shown > s.limit {
		shown = s.limit
	}

	header := fmt.Sprintf("🔍 <b>%s</b> [%s]\nПосилання: %s\nВсього оголошень: %d\nНе рекламних: %d\nНових: %d",
		escape(p.Source()), escape(p.Label()), escape(p.URL()), len(items), nonAd, len(fresh))
	if shown < len(fresh) {
		header += fmt.Sprintf("\nПоказано перших: %d", shown)
	}
	s.send(header)

	for _, it := range fresh[:shown] {
		s.send(renderWithNameUA(it, p.Source(), p.Label()))
	}
	s.send("━━━━━━━━━━━━━━━━━━━━")
	return res
}

// sleepBetweenRequests spaces out requests to avoid hammering the source.
func (s *Scheduler) sleepBetweenRequests() {
	if s.baseDelay <= 0 && s.jitter <= 0 {
		return
	}
	extra := time.Duration(0)
	if s.jitter > 0 {
		extra = time.Duration(rand.Int63n(int64(s.jitter)))
	}
	time.Sleep(s.baseDelay + extra)
}

func (s *Scheduler) send(text string) {
	if err := s.notifier.SendMessage(text); err != nil {
		log.Printf("scheduler: telegram send failed: %v", err)
	}
}

// runSummary renders the single wrap-up message sent after every run.
func runSummary(now time.Time, results []jobResult) string {
	totalNew := 0
	var failed []jobResult
	for _, r := range results {
		totalNew += r.new
		if r.err != nil {
			failed = append(failed, r)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🔄 <b>Перевірка %s</b>\nПошуків: %d\nНових оголошень: %d",
		now.Format("02.01 15:04"), len(results), totalNew)
	if len(failed) > 0 {
		fmt.Fprintf(&b, "\nПомилок: %d", len(failed))
		for _, r := range failed {
			fmt.Fprintf(&b, "\n• %s: %s", escape(r.label), escape(r.err.Error()))
		}
	}
	return b.String()
}

func isAdItem(it parser.Item) bool {
	// Check if item is an ad based on title or other indicators
	title := strings.ToLower(it.Title)
	price := strings.ToLower(it.Price)

	// Skip items with CSS content or very short titles
	if len(title) < 3 || strings.Contains(title, "css-") || strings.Contains(title, "{") {
		return true
	}

	// Check for ad indicators
	adIndicators := []string{
		"топ", "реклама", "преміум", "sponsored", "рекламне",
		"підняти", "підняти оголошення", "преміум оголошення",
		"виділити", "виділити оголошення", "популярне",
	}

	for _, indicator := range adIndicators {
		if strings.Contains(title, indicator) || strings.Contains(price, indicator) {
			return true
		}
	}

	// Check for suspicious patterns that might indicate ads
	suspiciousPatterns := []string{
		"безкоштовно", "даром", "віддам", "обмін", "бартер",
		"робота", "вакансія", "заробіток", "дохід",
		"бізнес", "бізнес можливість", "заробляй",
	}

	for _, pattern := range suspiciousPatterns {
		if strings.Contains(title, pattern) {
			return true
		}
	}

	return false
}

func renderWithNameUA(it parser.Item, source, name string) string {
	return fmt.Sprintf("📌 <b>[%s]</b> %s\n🏷️ Назва: %s\n💰 Ціна: %s\n📍 Локація: %s\n🔗 Посилання: %s",
		escape(source), escape(name), escape(it.Title), escape(it.Price), escape(it.Location), escape(it.URL))
}

// escape makes text safe for Telegram's HTML parse mode.
func escape(s string) string { return html.EscapeString(s) }
