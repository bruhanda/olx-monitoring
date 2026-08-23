package olx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

const searchPage = `<html><body>
<p data-testid="total-count">Знайдено 2 оголошення</p>
<div data-testid="l-card">
  <a href="/d/uk/obyavlenie/cherevyky-dr-martens-1460-IDxxx.html">Черевики Dr. Martens 1460</a>
  <p data-testid="ad-price">5 000 грн</p>
  <p>Київ, Поділ</p>
</div>
<div data-testid="l-card">
  <a href="https://www.olx.ua/d/uk/obyavlenie/krossivky-IDyyy.html?reason=extended_search">Кросівки Nike</a>
  <p data-testid="ad-price">2 000 грн</p>
</div>
<div data-testid="l-card">
  <a href="/d/uk/obyavlenie/sandali-IDzzz.html">Сандалі Rieker</a>
</div>
</body></html>`

func parse(t *testing.T, o *OLX, html string) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("failed to parse fixture: %v", err)
	}
	return doc
}

func TestParseHTML(t *testing.T) {
	o := NewWithName(http.DefaultClient, "", "https://www.olx.ua/uk/list/q-dr-martens-1460/", "Dr. Martens")
	items := o.parseHTML(parse(t, o, searchPage))

	if len(items) != 1 {
		t.Fatalf("got %d items, want 1: %+v", len(items), items)
	}
	it := items[0]
	if it.Title != "Черевики Dr. Martens 1460" {
		t.Errorf("title = %q", it.Title)
	}
	if it.URL != "https://www.olx.ua/d/uk/obyavlenie/cherevyky-dr-martens-1460-IDxxx.html" {
		t.Errorf("url = %q", it.URL)
	}
	if it.Price != "5 000 грн" {
		t.Errorf("price = %q", it.Price)
	}
	if it.Location != "Київ, Поділ" {
		t.Errorf("location = %q", it.Location)
	}
	if len(it.ExternalID) != 40 {
		t.Errorf("external id = %q, want sha1 hex", it.ExternalID)
	}
}

func TestGetRealCount(t *testing.T) {
	o := NewWithName(http.DefaultClient, "", "https://www.olx.ua/uk/list/q-test/", "test")
	if got := o.getRealCount(parse(t, o, searchPage)); got != 2 {
		t.Fatalf("getRealCount() = %d, want 2", got)
	}
}

func TestNoResultsPage(t *testing.T) {
	o := NewWithName(http.DefaultClient, "", "https://www.olx.ua/uk/list/q-test/", "test")
	doc := parse(t, o, `<html><body><p>Ми знайшли 0 оголошень</p></body></html>`)
	if items := o.parseHTML(doc); len(items) != 0 {
		t.Fatalf("got %d items, want 0", len(items))
	}
}

func TestExtractSearchKeywords(t *testing.T) {
	o := NewWithName(http.DefaultClient, "", "https://www.olx.ua/uk/list/q-dr-martens-1460/?currency=UAH", "x")
	got := o.extractSearchKeywords()
	want := []string{"martens", "1460"}
	if len(got) != len(want) {
		t.Fatalf("keywords = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keywords = %v, want %v", got, want)
		}
	}
}

// Fetch must send the configured user agent, not a hardcoded one.
func TestFetchUsesConfiguredUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(searchPage))
	}))
	defer srv.Close()

	o := NewWithName(srv.Client(), "InfoParserBot/2.0", srv.URL+"/list/q-dr-martens-1460/", "Dr. Martens")
	items, err := o.Fetch()
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}
	if gotUA != "InfoParserBot/2.0" {
		t.Fatalf("User-Agent = %q, want %q", gotUA, "InfoParserBot/2.0")
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}

	// Empty user agent falls back to a browser-like default.
	o = NewWithName(srv.Client(), "", srv.URL+"/list/q-dr-martens-1460/", "Dr. Martens")
	if _, err := o.Fetch(); err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}
	if gotUA != defaultUserAgent {
		t.Fatalf("User-Agent = %q, want default", gotUA)
	}
}

func TestFetchRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	o := NewWithName(srv.Client(), "", srv.URL, "test")
	if _, err := o.Fetch(); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("Fetch() error = %v, want status 403", err)
	}
}

// OLX інлайнить emotion-CSS у <style> прямо всередині <a> із заголовком.
// goquery.Text() включає вміст <style>, тож заголовок ставав CSS і картка
// мовчки зникала зі звіту.
const cardWithInlineStyle = `<html><body>
<p data-testid="total-count">1</p>
<div data-testid="l-card">
  <style data-emotion="css ri9uxm">.css-ri9uxm{height:100%;background:var(--colorsBackgroundPrimary);}</style>
  <a href="/d/uk/obyavlenie/olimpiyka-reebok-IDabc.html?search_reason=search%7Corganic">
    <style data-emotion="css hvzem4">.css-hvzem4{display:flex;flex-direction:row;}</style>
    Олімпійка Reebok чоловіча, розмір S
  </a>
  <p data-testid="ad-price">450 грн</p>
  <p>Київ, Оболонь</p>
</div>
</body></html>`

func TestParseHTMLIgnoresInlineStyleTags(t *testing.T) {
	o := NewWithName(http.DefaultClient, "", "https://www.olx.ua/uk/list/q-reebok/", "Reebok")
	items := o.parseHTML(parse(t, o, cardWithInlineStyle))

	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 — картку з інлайновим <style> відкинуто", len(items))
	}
	if items[0].Title != "Олімпійка Reebok чоловіча, розмір S" {
		t.Errorf("title = %q", items[0].Title)
	}
	if items[0].Price != "450 грн" {
		t.Errorf("price = %q", items[0].Price)
	}
}

// Кириличні запити приходять percent-encoded; без декодування жоден заголовок
// не збігався з ключовими словами і всі картки вважались «альтернативними».
func TestExtractSearchKeywordsDecodesCyrillic(t *testing.T) {
	raw := "https://www.olx.ua/uk/moda-i-stil/muzhskaya-odezhda/q-%D0%BE%D0%BB%D1%96%D0%BC%D0%BF%D1%96%D0%B9%D0%BA%D0%B0-reebok/?currency=UAH"
	o := NewWithName(http.DefaultClient, "", raw, "Олімпійка Reebok")

	got := o.extractSearchKeywords()
	want := []string{"олімпійка", "reebok"}
	if len(got) != len(want) {
		t.Fatalf("keywords = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keywords = %v, want %v", got, want)
		}
	}

	// Оголошення без латинської назви бренду більше не відкидається.
	if o.isAlternativeListing("Олімпійка чоловіча спортивна, розмір S", "/d/uk/obyavlenie/x.html") {
		t.Error("оголошення з кириличним ключовим словом відкинуто як альтернативне")
	}
}
