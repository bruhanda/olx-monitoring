package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bruhanda/olx-monitoring/internal/storage"
)

func newServer(t *testing.T, opts Options) (*Server, *storage.Store) {
	t.Helper()
	store := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	return New(store, opts), store
}

func post(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The edit form used to interpolate query parameters into raw HTML.
func TestEditFormEscapesStoredValues(t *testing.T) {
	s, store := newServer(t, Options{})
	if _, err := store.CreateSearchIfNotExists(`<script>alert(1)</script>`, "https://olx.ua/q-x/?a=1&b=2", "olx", true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/edit?id=1", nil)
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("script tag was rendered unescaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("escaped name missing from form: %s", body)
	}
}

// Query parameters must not drive what the edit form shows.
func TestEditFormIgnoresQueryValues(t *testing.T) {
	s, store := newServer(t, Options{})
	if _, err := store.CreateSearchIfNotExists("real name", "https://olx.ua/q-x/", "olx", true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/edit?id=1&name=injected&url=injected", nil)
	s.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "injected") {
		t.Fatal("query values leaked into the form")
	}
	if !strings.Contains(body, "real name") {
		t.Fatal("stored name missing from the form")
	}
}

func TestEditFormUnknownID(t *testing.T) {
	s, _ := newServer(t, Options{})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/edit?id=42", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestClearListingsByID(t *testing.T) {
	s, store := newServer(t, Options{})
	if _, err := store.CreateSearchIfNotExists("x", "https://olx.ua/q-x/", "olx", true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := store.CreateListingIfNotExists(&storage.Listing{SearchID: 1, Source: "olx", ExternalID: "a", URL: "https://olx.ua/d/uk/obyavlenie/a.html"}); err != nil {
		t.Fatalf("seed listing: %v", err)
	}

	rec := post(t, s.Handler(), "/clear", url.Values{"id": {"1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}

	created, err := store.CreateListingIfNotExists(&storage.Listing{SearchID: 1, Source: "olx", ExternalID: "a"})
	if err != nil || !created {
		t.Fatalf("listing was not cleared: created=%v err=%v", created, err)
	}
}

func TestAddSearchValidatesURL(t *testing.T) {
	s, store := newServer(t, Options{})

	if rec := post(t, s.Handler(), "/add", url.Values{"name": {"x"}, "url": {"olx.ua/q-x/"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if rec := post(t, s.Handler(), "/add", url.Values{"name": {"x"}, "url": {"https://olx.ua/q-x/"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	list, err := store.ListAllSearches("olx")
	if err != nil || len(list) != 1 {
		t.Fatalf("searches = %+v, err = %v", list, err)
	}
}

func TestMutationsRejectGET(t *testing.T) {
	s, _ := newServer(t, Options{})
	for _, path := range []string{"/add", "/delete", "/clear", "/activate", "/deactivate"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s = %d, want 405", path, rec.Code)
		}
	}
}

func TestBasicAuth(t *testing.T) {
	s, _ := newServer(t, Options{Username: "admin", Password: "s3cret"})
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "wrong")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad password status = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want 200", rec.Code)
	}
}

func TestIndexShowsSchedule(t *testing.T) {
	s, _ := newServer(t, Options{NotifyTimes: []string{"11:00", "15:00"}})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), "11:00, 15:00") {
		t.Fatal("configured schedule is not shown on the index page")
	}
}

// The health endpoint must answer even when the admin UI is behind basic auth.
func TestHealthzBypassesAuth(t *testing.T) {
	for _, opts := range []Options{{}, {Username: "admin", Password: "s3cret"}} {
		s, _ := newServer(t, opts)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Fatalf("healthz = %d %q (auth=%v)", rec.Code, rec.Body.String(), opts.Username != "")
		}
	}
}
