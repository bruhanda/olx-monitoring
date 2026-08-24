package scheduler

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bruhanda/olx-monitoring/internal/parser"
)

// Regression test: the previous hand-rolled replaceAll looped forever on "&"
// because it kept finding the ampersand of the just-inserted "&amp;".
func TestEscapeTerminatesOnAmpersand(t *testing.T) {
	done := make(chan string, 1)
	go func() {
		done <- escape(`Dr. Martens & Co <b>"1460"</b>`)
	}()

	select {
	case got := <-done:
		want := "Dr. Martens &amp; Co &lt;b&gt;&#34;1460&#34;&lt;/b&gt;"
		if got != want {
			t.Fatalf("escape() = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("escape() did not terminate")
	}
}

func TestRenderWithNameUAEscapesEveryField(t *testing.T) {
	out := renderWithNameUA(parser.Item{
		Title:    "Куртка <M&M>",
		Price:    "1 000 грн",
		Location: "Київ, Поділ",
		URL:      "https://www.olx.ua/d/uk/obyavlenie/x.html?a=1&b=2",
	}, "olx", "Пошук & тест")

	if strings.Contains(out, "<M&M>") || strings.Contains(out, "a=1&b=2") {
		t.Fatalf("unescaped content in message: %s", out)
	}
	for _, want := range []string{"&lt;M&amp;M&gt;", "a=1&amp;b=2", "Пошук &amp; тест", "1 000 грн"} {
		if !strings.Contains(out, want) {
			t.Fatalf("message %q is missing %q", out, want)
		}
	}
}

func TestNextOccurrence(t *testing.T) {
	loc := time.FixedZone("EET", 2*60*60)
	times := []string{"11:00", "15:00", "20:00"}

	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"before first", time.Date(2026, 8, 20, 9, 30, 0, 0, loc), time.Date(2026, 8, 20, 11, 0, 0, 0, loc)},
		{"between slots", time.Date(2026, 8, 20, 12, 0, 0, 0, loc), time.Date(2026, 8, 20, 15, 0, 0, 0, loc)},
		{"exactly on slot", time.Date(2026, 8, 20, 15, 0, 0, 0, loc), time.Date(2026, 8, 20, 15, 0, 0, 0, loc)},
		{"after last rolls over", time.Date(2026, 8, 20, 21, 0, 0, 0, loc), time.Date(2026, 8, 21, 11, 0, 0, 0, loc)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextOccurrence(tc.now, times); !got.Equal(tc.want) {
				t.Fatalf("nextOccurrence(%s) = %s, want %s", tc.now, got, tc.want)
			}
		})
	}
}

func TestIsAdItem(t *testing.T) {
	cases := []struct {
		title string
		price string
		want  bool
	}{
		{"Черевики Dr. Martens 1460", "5 000 грн", false},
		{"ТОП оголошення", "5 000 грн", true},
		{"Робота у Польщі", "", true},
		{"ab", "", true},
		{"css-1abcdef", "", true},
	}
	for _, tc := range cases {
		got := isAdItem(parser.Item{Title: tc.title, Price: tc.price})
		if got != tc.want {
			t.Errorf("isAdItem(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

func TestRunSummary(t *testing.T) {
	now := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)

	quiet := runSummary(now, []jobResult{{label: "a"}, {label: "b"}})
	if !strings.Contains(quiet, "Нових оголошень: 0") || strings.Contains(quiet, "Помилок") {
		t.Fatalf("unexpected quiet summary: %s", quiet)
	}

	noisy := runSummary(now, []jobResult{
		{label: "a", new: 3},
		{label: "b & c", err: errors.New("unexpected status: 403")},
	})
	for _, want := range []string{"Пошуків: 2", "Нових оголошень: 3", "Помилок: 1", "b &amp; c", "403"} {
		if !strings.Contains(noisy, want) {
			t.Fatalf("summary %q is missing %q", noisy, want)
		}
	}
}

// Коли падають геть усі пошуки з однією й тією ж помилкою, у звіті має бути
// один рядок, а не стіна з однакових.
func TestRunSummaryGroupsIdenticalErrors(t *testing.T) {
	now := time.Date(2026, 8, 24, 20, 0, 0, 0, time.UTC)
	blocked := errors.New("unexpected status: 403")

	var results []jobResult
	for _, name := range []string{"Meermin", "Loake", "Solovair", "Red Wing", "Wolverine"} {
		results = append(results, jobResult{label: name, err: blocked})
	}
	out := runSummary(now, results)

	if strings.Count(out, "unexpected status: 403") != 1 {
		t.Fatalf("помилка має згадуватись один раз:\n%s", out)
	}
	if !strings.Contains(out, "• усі пошуки: unexpected status: 403") {
		t.Errorf("немає згорнутого рядка про всі пошуки:\n%s", out)
	}
	if !strings.Contains(out, "блокування за IP") {
		t.Errorf("немає підказки про причину 403:\n%s", out)
	}
	for _, name := range []string{"Meermin", "Loake"} {
		if strings.Contains(out, name) {
			t.Errorf("назви пошуків зайві, коли впали всі: %s", out)
		}
	}
}

func TestRunSummaryGroupsPartialFailures(t *testing.T) {
	now := time.Date(2026, 8, 24, 20, 0, 0, 0, time.UTC)
	blocked := errors.New("unexpected status: 403")

	results := []jobResult{
		{label: "ok-1", new: 2},
		{label: "a", err: blocked}, {label: "b", err: blocked},
		{label: "c", err: blocked}, {label: "d", err: blocked},
		{label: "e", err: errors.New("timeout")},
	}
	out := runSummary(now, results)

	if !strings.Contains(out, "• 4 пошуків: unexpected status: 403") {
		t.Errorf("однакові помилки не згорнуті:\n%s", out)
	}
	if !strings.Contains(out, "• e: timeout") {
		t.Errorf("поодинока помилка має лишатись іменною:\n%s", out)
	}
	if !strings.Contains(out, "Помилок: 5") || !strings.Contains(out, "Нових оголошень: 2") {
		t.Errorf("невірні підсумкові числа:\n%s", out)
	}
}

// Розклад читається перед кожним очікуванням, а Reload() будить планувальник
// одразу — інакше зміна з веб-адмінки застосувалась би лише після рестарту.
func TestRunRereadsScheduleOnReload(t *testing.T) {
	var reads int32
	getTimes := func() []string {
		atomic.AddInt32(&reads, 1)
		return []string{"03:00"} // далеко попереду, само не спрацює
	}
	var runs int32
	jobs := func() ([]Job, error) {
		atomic.AddInt32(&runs, 1)
		return nil, nil
	}

	s := New(nil, nil, time.Hour, jobs).WithTimes(getTimes)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { s.Run(stop); close(done) }()
	defer func() {
		close(stop)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run() не завершився після stop")
		}
	}()

	waitFor(t, &reads, 1, "розклад не прочитано на старті")

	s.Reload()
	waitFor(t, &reads, 2, "розклад не перечитано після Reload()")

	if n := atomic.LoadInt32(&runs); n != 0 {
		t.Fatalf("Reload() не має запускати перевірку, а прогонів %d", n)
	}
}

// Без розкладу планувальник працює за інтервалом і робить перший прогін одразу
// — на цьому тримається процедура прогріву бази з DEPLOY.md.
func TestRunIntervalModeRunsImmediately(t *testing.T) {
	var runs int32
	jobs := func() ([]Job, error) {
		atomic.AddInt32(&runs, 1)
		return nil, nil
	}
	s := New(nil, nil, time.Hour, jobs).WithTimes(func() []string { return nil })

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { s.Run(stop); close(done) }()

	waitFor(t, &runs, 1, "у режимі інтервалу перший прогін має бути одразу")

	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() не завершився після stop")
	}
}

func waitFor(t *testing.T, counter *int32, want int32, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(counter) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s (лічильник %d, очікувалось %d)", msg, atomic.LoadInt32(counter), want)
}

func TestReloadIsNonBlocking(t *testing.T) {
	s := New(nil, nil, time.Hour, func() ([]Job, error) { return nil, nil })
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			s.Reload() // ніхто не слухає — не має блокувати
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Reload() заблокувався без читача")
	}
}
