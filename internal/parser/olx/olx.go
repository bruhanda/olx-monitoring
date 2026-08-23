package olx

import (
	"crypto/sha1"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/bruhanda/olx-monitoring/internal/parser"
)

// defaultUserAgent is used when no HTTP_USER_AGENT is configured. OLX serves an
// empty listing page to obviously non-browser clients.
const defaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// debug enables per-card logging (including raw HTML) for troubleshooting
// markup changes on OLX. Enable with OLX_DEBUG=1.
var debug = os.Getenv("OLX_DEBUG") == "1"

func debugf(format string, args ...any) {
	if debug {
		log.Printf(format, args...)
	}
}

type OLX struct {
	client    *http.Client
	userAgent string
	searchURL string
	name      string
}

func New(client *http.Client, userAgent, searchURL string) *OLX {
	return NewWithName(client, userAgent, searchURL, searchURL)
}

func NewWithName(client *http.Client, userAgent, searchURL, name string) *OLX {
	if name == "" {
		name = searchURL
	}
	if strings.TrimSpace(userAgent) == "" {
		userAgent = defaultUserAgent
	}
	return &OLX{client: client, userAgent: userAgent, searchURL: searchURL, name: name}
}

func (o *OLX) Source() string { return "olx" }

func (o *OLX) Label() string { return o.name }

func (o *OLX) URL() string { return o.searchURL }

func (o *OLX) Fetch() ([]parser.Item, error) {
	target := o.searchURL
	debugf("[OLX] Fetching: %s", target)

	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", o.userAgent)
	req.Header.Set("Accept-Language", "uk-UA,uk;q=0.9,en;q=0.8")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	items := o.parseHTML(doc)
	log.Printf("[OLX] %s: extracted %d items (OLX reports %d)", o.name, len(items), o.getRealCount(doc))

	return items, nil
}

func (o *OLX) parseHTML(doc *goquery.Document) []parser.Item {
	var items []parser.Item

	realCount := o.getRealCount(doc)
	debugf("[OLX] Real count from OLX: %d", realCount)

	// Check if OLX shows "no results" message and real count is 0
	if realCount == 0 {
		noResultsText := doc.Find("body").Text()
		if strings.Contains(noResultsText, "Ми знайшли 0 оголошень") ||
			strings.Contains(noResultsText, "Знайдено 0 оголошень") ||
			strings.Contains(noResultsText, "Немає оголошень") {
			debugf("[OLX] OLX shows no results for this search")
			return items
		}
	}

	doc.Find("[data-testid='l-card']").Each(func(i int, card *goquery.Selection) {
		// OLX інлайнить emotion-CSS у <style> всередині самої картки, зокрема
		// всередині <a> із заголовком. goquery.Text() включає вміст <style>,
		// тож без цього кроку заголовок перетворюється на CSS і картка
		// відкидається як сміття.
		s := card.Clone()
		s.Find("style, script, noscript").Remove()

		if debug {
			html, _ := s.Html()
			if len(html) > 500 {
				html = html[:500] + "..."
			}
			debugf("[OLX] Listing %d HTML: %s", i+1, html)
		}

		// URL - look for links to listings
		var href string
		s.Find("a[href*='/d/uk/obyavlenie/']").EachWithBreak(func(_ int, link *goquery.Selection) bool {
			if url, exists := link.Attr("href"); exists && url != "" {
				href = url
				if !strings.HasPrefix(href, "http") {
					href = "https://www.olx.ua" + href
				}
				return false
			}
			return true
		})

		// Title - text of the listing link
		var title string
		s.Find("a[href*='/d/uk/obyavlenie/']").EachWithBreak(func(_ int, link *goquery.Selection) bool {
			candidate := cleanText(strings.TrimSpace(link.Text()))
			if len(candidate) > 3 {
				title = candidate
				return false
			}
			return true
		})

		// Price
		var price string
		s.Find("[data-testid='ad-price']").EachWithBreak(func(_ int, priceElem *goquery.Selection) bool {
			price = cleanText(strings.TrimSpace(priceElem.Text()))
			return price == ""
		})

		// Location - heuristic over short text nodes
		var location string
		s.Find("p, span, div").EachWithBreak(func(_ int, elem *goquery.Selection) bool {
			text := cleanText(strings.TrimSpace(elem.Text()))
			if len(text) > 0 && len(text) < 100 {
				if strings.Contains(text, ",") || strings.Contains(text, "р.") || strings.Contains(text, "Сьогодні") ||
					strings.Contains(text, "Київ") || strings.Contains(text, "Львів") || strings.Contains(text, "Харків") {
					location = text
					return false
				}
			}
			return true
		})

		if href == "" || len(title) < 3 {
			debugf("[OLX] Skipping item %d - missing href or title", i+1)
			return
		}

		// Skip alternative listings that don't match the search query
		if o.isAlternativeListing(title, href) {
			debugf("[OLX] Skipping item %d - alternative listing not matching search", i+1)
			return
		}

		items = append(items, parser.Item{
			ExternalID: hash(href),
			Title:      title,
			URL:        href,
			Price:      price,
			Location:   location,
			PostedAt:   time.Now(),
		})
		debugf("[OLX] Successfully extracted item %d: %s", i+1, title)
	})

	return items
}

// getRealCount extracts the total listing count reported by OLX itself.
func (o *OLX) getRealCount(doc *goquery.Document) int {
	selectors := []string{
		"[data-testid='total-count']",
		"[data-testid='search-results-count']",
		".css-1sw7q4x span", // Common OLX class
		"span:contains('оголошень')",
		"span:contains('знайдено')",
	}

	for _, selector := range selectors {
		element := doc.Find(selector)
		if element.Length() == 0 {
			continue
		}
		countText := strings.TrimSpace(element.First().Text())
		debugf("[OLX] Found count with selector '%s': '%s'", selector, countText)
		if count, ok := digitsToInt(countText); ok {
			return count
		}
	}

	debugf("[OLX] Could not find total count element")
	return 0
}

// digitsToInt keeps only digits of s and parses them, e.g. "Знайдено 10 оголошень" -> 10.
func digitsToInt(s string) (int, bool) {
	var digits strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	if digits.Len() == 0 {
		return 0, false
	}
	count, err := strconv.Atoi(digits.String())
	if err != nil {
		return 0, false
	}
	return count, true
}

// isAlternativeListing checks if the listing is an alternative suggestion that doesn't match the search query
func (o *OLX) isAlternativeListing(title, url string) bool {
	// Check if URL contains extended search indicators
	if strings.Contains(url, "reason=extended_search_no_results_last_resort") ||
		strings.Contains(url, "reason=extended_search") {
		return true
	}

	// Extract search keywords from the original URL
	searchKeywords := o.extractSearchKeywords()
	if len(searchKeywords) == 0 {
		return false
	}

	// Check if title contains any of the search keywords
	titleLower := strings.ToLower(title)
	for _, keyword := range searchKeywords {
		if strings.Contains(titleLower, strings.ToLower(keyword)) {
			return false // This listing matches the search
		}
	}

	// If no keywords match, it's likely an alternative listing
	return true
}

// extractSearchKeywords extracts search keywords from the URL
func (o *OLX) extractSearchKeywords() []string {
	var keywords []string

	// Extract from q- parameter
	if strings.Contains(o.searchURL, "q-") {
		parts := strings.Split(o.searchURL, "q-")
		if len(parts) > 1 {
			query := strings.Split(parts[1], "/")[0]
			query = strings.Split(query, "?")[0]
			// Кириличні запити приходять percent-encoded ("%D0%BE..."), і без
			// декодування жоден заголовок із ними не збігається — усі картки
			// відкидались би як «альтернативні».
			if decoded, err := url.QueryUnescape(query); err == nil {
				query = decoded
			}
			// Split by hyphens and underscores
			for _, part := range strings.FieldsFunc(query, func(r rune) bool {
				return r == '-' || r == '_'
			}) {
				if len(part) > 2 { // Only meaningful keywords
					keywords = append(keywords, part)
				}
			}
		}
	}

	return keywords
}

func cleanText(s string) string {
	// Remove CSS content and clean up text
	s = strings.TrimSpace(s)
	if strings.Contains(s, "{") || strings.Contains(s, "css-") || strings.Contains(s, "background-color") {
		return ""
	}
	// Remove excessive whitespace
	return strings.Join(strings.Fields(s), " ")
}

func hash(s string) string {
	h := sha1.Sum([]byte(s))
	return fmt.Sprintf("%x", h[:])
}
