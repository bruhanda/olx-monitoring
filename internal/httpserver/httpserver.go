package httpserver

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bruhanda/olx-monitoring/internal/storage"
)

// Options configures the admin web UI.
type Options struct {
	NotifyTimes []string // schedule shown on the index page
	Username    string   // HTTP basic auth user; empty disables authentication
	Password    string
}

type Server struct {
	store *storage.Store
	opts  Options
}

func New(store *storage.Store, opts Options) *Server {
	return &Server{store: store, opts: opts}
}

var (
	indexTmpl    = template.Must(template.New("index").Parse(indexTemplate))
	editTmpl     = template.Must(template.New("edit").Parse(editTemplate))
	listingsTmpl = template.Must(template.New("listings").Parse(listingsTemplate))
)

// perPage — скільки оголошень показувати на сторінці /listings.
const perPage = 50

// Handler builds the admin router. It uses a dedicated mux instead of
// http.DefaultServeMux and wraps it in optional basic auth.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.indexHandler)
	mux.HandleFunc("/add", s.addSearchHandler)
	mux.HandleFunc("/delete", s.deleteSearchHandler)
	mux.HandleFunc("/clear", s.clearListingsHandler)
	mux.HandleFunc("/deactivate", s.deactivateSearchHandler)
	mux.HandleFunc("/activate", s.activateSearchHandler)
	mux.HandleFunc("/edit", s.editSearchHandler)
	mux.HandleFunc("/listings", s.listingsHandler)
	mux.HandleFunc("/clear-orphans", s.clearOrphansHandler)
	mux.HandleFunc(healthPath, healthHandler)
	return s.withAuth(mux)
}

func (s *Server) Start(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if s.opts.Username == "" {
		log.Printf("HTTP server starting on %s (authentication disabled)", addr)
	} else {
		log.Printf("HTTP server starting on %s (basic auth enabled)", addr)
	}
	return srv.ListenAndServe()
}

// healthPath is served without authentication so that a reverse proxy or an
// external monitor can check liveness.
const healthPath = "/healthz"

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok"))
}

// withAuth guards the admin UI with HTTP basic auth when credentials are set.
func (s *Server) withAuth(next http.Handler) http.Handler {
	if s.opts.Username == "" && s.opts.Password == "" {
		return next
	}
	wantUser := []byte(s.opts.Username)
	wantPass := []byte(s.opts.Password)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == healthPath {
			next.ServeHTTP(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(user), wantUser) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), wantPass) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="info-parser", charset="UTF-8"`)
			http.Error(w, "Потрібна авторизація", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// schedule renders the configured notification times for the index page.
func (s *Server) schedule() string {
	if len(s.opts.NotifyTimes) == 0 {
		return "постійно, за інтервалом опитування"
	}
	return strings.Join(s.opts.NotifyTimes, ", ")
}

func (s *Server) indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	searches, err := s.store.ListAllSearches("olx")
	if err != nil {
		log.Printf("httpserver: failed to list searches: %v", err)
		http.Error(w, "Помилка завантаження пошуків", http.StatusInternalServerError)
		return
	}

	counts, err := s.store.CountListingsBySearch()
	if err != nil {
		log.Printf("httpserver: failed to count listings: %v", err)
		counts = map[uint]int64{} // сторінка корисна й без лічильників
	}

	type searchRow struct {
		storage.SavedSearch
		Listings int64
	}
	rows := make([]searchRow, 0, len(searches))
	for _, sr := range searches {
		rows = append(rows, searchRow{SavedSearch: sr, Listings: counts[sr.ID]})
	}

	orphans, err := s.store.CountOrphanListings()
	if err != nil {
		log.Printf("httpserver: failed to count orphan listings: %v", err)
	}

	data := struct {
		Searches []searchRow
		Schedule string
		Orphans  int64
	}{
		Searches: rows,
		Schedule: s.schedule(),
		Orphans:  orphans,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, data); err != nil {
		log.Printf("httpserver: failed to render index: %v", err)
	}
}

// formID reads and validates an id form value.
func formID(r *http.Request, value string) (uint, error) {
	if value == "" {
		return 0, errors.New("ID не вказано")
	}
	id, err := strconv.ParseUint(value, 10, 32)
	if err != nil || id == 0 {
		return 0, errors.New("Невірний ID")
	}
	return uint(id), nil
}

// requirePost rejects anything but POST.
func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "Метод не підтримується", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// byID runs an action that takes a saved search id from the "id" form value.
func (s *Server) byID(w http.ResponseWriter, r *http.Request, what string, action func(uint) error) {
	if !requirePost(w, r) {
		return
	}
	id, err := formID(r, r.FormValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := action(id); err != nil {
		log.Printf("httpserver: %s failed for id=%d: %v", what, id, err)
		http.Error(w, fmt.Sprintf("Помилка (%s): %v", what, err), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) addSearchHandler(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	url := strings.TrimSpace(r.FormValue("url"))
	if name == "" || url == "" {
		http.Error(w, "Назва та посилання обов'язкові", http.StatusBadRequest)
		return
	}
	if !isValidSearchURL(url) {
		http.Error(w, "Посилання має починатися з http:// або https://", http.StatusBadRequest)
		return
	}

	if _, err := s.store.CreateSearchIfNotExists(name, url, "olx", true); err != nil {
		log.Printf("httpserver: failed to create search: %v", err)
		http.Error(w, fmt.Sprintf("Помилка збереження: %v", err), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) deleteSearchHandler(w http.ResponseWriter, r *http.Request) {
	s.byID(w, r, "видалення", s.store.DeleteSearchByID)
}

func (s *Server) clearListingsHandler(w http.ResponseWriter, r *http.Request) {
	s.byID(w, r, "очищення", s.store.ClearListingsForSearchID)
}

func (s *Server) deactivateSearchHandler(w http.ResponseWriter, r *http.Request) {
	s.byID(w, r, "деактивації", s.store.DeactivateSearch)
}

func (s *Server) activateSearchHandler(w http.ResponseWriter, r *http.Request) {
	s.byID(w, r, "активації", s.store.ActivateSearch)
}

func (s *Server) editSearchHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Values come from the database, never straight from the query string.
		id, err := formID(r, r.URL.Query().Get("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		search, err := s.store.GetSearchByID(id)
		if errors.Is(err, storage.ErrSearchNotFound) {
			http.Error(w, "Пошук не знайдено", http.StatusNotFound)
			return
		}
		if err != nil {
			log.Printf("httpserver: failed to load search %d: %v", id, err)
			http.Error(w, "Помилка завантаження пошуку", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := editTmpl.Execute(w, search); err != nil {
			log.Printf("httpserver: failed to render edit form: %v", err)
		}

	case http.MethodPost:
		id, err := formID(r, r.FormValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		url := strings.TrimSpace(r.FormValue("url"))
		if name == "" || url == "" {
			http.Error(w, "Всі поля обов'язкові", http.StatusBadRequest)
			return
		}
		if !isValidSearchURL(url) {
			http.Error(w, "Посилання має починатися з http:// або https://", http.StatusBadRequest)
			return
		}

		if err := s.store.UpdateSearch(id, name, url); err != nil {
			log.Printf("httpserver: failed to update search %d: %v", id, err)
			http.Error(w, fmt.Sprintf("Помилка оновлення: %v", err), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)

	default:
		http.Error(w, "Метод не підтримується", http.StatusMethodNotAllowed)
	}
}

// clearOrphansHandler видаляє оголошення, які не належать жодному пошуку.
func (s *Server) clearOrphansHandler(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	deleted, err := s.store.DeleteOrphanListings()
	if err != nil {
		log.Printf("httpserver: failed to delete orphan listings: %v", err)
		http.Error(w, fmt.Sprintf("Помилка видалення: %v", err), http.StatusInternalServerError)
		return
	}
	log.Printf("httpserver: deleted %d orphan listings", deleted)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// listingsHandler показує зібрані оголошення одного пошуку, найновіші зверху.
func (s *Server) listingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Метод не підтримується", http.StatusMethodNotAllowed)
		return
	}

	id, err := formID(r, r.URL.Query().Get("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	search, err := s.store.GetSearchByID(id)
	if errors.Is(err, storage.ErrSearchNotFound) {
		http.Error(w, "Пошук не знайдено", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("httpserver: failed to load search %d: %v", id, err)
		http.Error(w, "Помилка завантаження пошуку", http.StatusInternalServerError)
		return
	}

	page := 1
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 1 {
			page = n
		}
	}

	items, total, err := s.store.ListListingsForSearch(id, perPage, (page-1)*perPage)
	if err != nil {
		log.Printf("httpserver: failed to list listings for %d: %v", id, err)
		http.Error(w, "Помилка завантаження оголошень", http.StatusInternalServerError)
		return
	}

	type listingRow struct {
		storage.Listing
		FirstSeen string
	}
	rows := make([]listingRow, 0, len(items))
	for _, it := range items {
		rows = append(rows, listingRow{Listing: it, FirstSeen: it.CreatedAt.Local().Format("02.01.2006 15:04")})
	}

	pages := int((total + perPage - 1) / perPage)
	data := struct {
		Search   storage.SavedSearch
		Listings []listingRow
		Total    int64
		Page     int
		Pages    int
		HasPrev  bool
		HasNext  bool
		PrevPage int
		NextPage int
	}{
		Search:   search,
		Listings: rows,
		Total:    total,
		Page:     page,
		Pages:    pages,
		HasPrev:  page > 1,
		HasNext:  page < pages,
		PrevPage: page - 1,
		NextPage: page + 1,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := listingsTmpl.Execute(w, data); err != nil {
		log.Printf("httpserver: failed to render listings: %v", err)
	}
}

func isValidSearchURL(url string) bool {
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}

const indexTemplate = `

<!DOCTYPE html>
<html lang="uk">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Пошук оголошень OLX</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        body {
            font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            padding: 20px;
        }
        
        .container {
            max-width: 800px;
            margin: 0 auto;
            background: white;
            border-radius: 15px;
            box-shadow: 0 20px 40px rgba(0,0,0,0.1);
            overflow: hidden;
        }
        
        .header {
            background: linear-gradient(135deg, #0198d0 0%, #0077b3 100%);
            color: white;
            padding: 30px;
            text-align: center;
        }
        
        .header h1 {
            font-size: 2.5em;
            margin-bottom: 10px;
            font-weight: 300;
        }
        
        .header p {
            font-size: 1.1em;
            opacity: 0.9;
            line-height: 1.6;
        }
        
        .content {
            padding: 30px;
        }
        
        .form-section {
            background: #f8f9fa;
            border-radius: 10px;
            padding: 25px;
            margin-bottom: 30px;
        }
        
        .form-section h2 {
            color: #333;
            margin-bottom: 20px;
            font-size: 1.5em;
            border-bottom: 2px solid #0198d0;
            padding-bottom: 10px;
        }
        
        .form-group {
            margin-bottom: 20px;
        }
        
        .form-group label {
            display: block;
            margin-bottom: 8px;
            font-weight: 600;
            color: #555;
        }
        
        .form-group input[type="text"] {
            width: 100%;
            padding: 12px 15px;
            border: 2px solid #e1e5e9;
            border-radius: 8px;
            font-size: 16px;
            transition: border-color 0.3s ease;
        }
        
        .form-group input[type="text"]:focus {
            outline: none;
            border-color: #0198d0;
            box-shadow: 0 0 0 3px rgba(1, 152, 208, 0.1);
        }
        
        .btn {
            background: linear-gradient(135deg, #0198d0 0%, #0077b3 100%);
            color: white;
            border: none;
            padding: 12px 25px;
            border-radius: 8px;
            font-size: 16px;
            font-weight: 600;
            cursor: pointer;
            transition: transform 0.2s ease, box-shadow 0.2s ease;
        }
        
        .btn:hover {
            transform: translateY(-2px);
            box-shadow: 0 5px 15px rgba(1, 152, 208, 0.3);
        }
        
        .btn-danger {
            background: linear-gradient(135deg, #dc3545 0%, #c82333 100%);
        }
        
        .btn-danger:hover {
            box-shadow: 0 5px 15px rgba(220, 53, 69, 0.3);
        }
        
        .btn-warning {
            background: linear-gradient(135deg, #ffc107 0%, #e0a800 100%);
            color: #212529;
        }
        
        .btn-warning:hover {
            box-shadow: 0 5px 15px rgba(255, 193, 7, 0.3);
        }
        
        .btn-secondary {
            background: linear-gradient(135deg, #6c757d 0%, #545b62 100%);
        }
        
        .btn-secondary:hover {
            box-shadow: 0 5px 15px rgba(108, 117, 125, 0.3);
        }
        
        .btn-success {
            background: linear-gradient(135deg, #28a745 0%, #1e7e34 100%);
        }
        
        .btn-success:hover {
            box-shadow: 0 5px 15px rgba(40, 167, 69, 0.3);
        }
        
        .btn-info {
            background: linear-gradient(135deg, #17a2b8 0%, #138496 100%);
        }
        
        .btn-info:hover {
            box-shadow: 0 5px 15px rgba(23, 162, 184, 0.3);
        }
        
        .search-actions {
            display: flex;
            flex-direction: column;
            gap: 8px;
            align-items: flex-end;
            min-width: 200px;
        }
        
        .search-actions .btn {
            width: 100%;
            text-align: center;
            padding: 8px 12px;
            font-size: 14px;
        }
        
        .searches-section {
            margin-top: 30px;
        }
        
        .search-item {
            background: white;
            border: 1px solid #e1e5e9;
            border-radius: 10px;
            padding: 20px;
            margin-bottom: 15px;
            display: flex;
            justify-content: space-between;
            align-items: flex-start;
            transition: box-shadow 0.3s ease;
        }
        
        .search-item:hover {
            box-shadow: 0 5px 15px rgba(0,0,0,0.1);
        }
        
        .search-info {
            flex: 1;
            margin-right: 20px;
        }
        
        .search-info h3 {
            color: #333;
            margin-bottom: 5px;
            font-size: 1.2em;
        }
        
        .search-info p {
            color: #666;
            font-size: 0.9em;
            word-break: break-all;
        }
        
        .empty-state {
            text-align: center;
            padding: 40px;
            color: #666;
        }
        
        .empty-state p {
            font-size: 1.1em;
            margin-bottom: 20px;
        }
        
        .info-box {
            background: linear-gradient(135deg, #e3f2fd 0%, #bbdefb 100%);
            border-left: 4px solid #0198d0;
            padding: 20px;
            margin-bottom: 25px;
            border-radius: 8px;
        }
        
        .info-box h3 {
            color: #1976d2;
            margin-bottom: 10px;
            font-size: 1.3em;
        }
        
        .info-box ul {
            margin-left: 20px;
            line-height: 1.6;
        }
        
        .info-box li {
            margin-bottom: 5px;
            color: #424242;
        }
        
        @media (max-width: 600px) {
            .container {
                margin: 10px;
                border-radius: 10px;
            }
            
            .header {
                padding: 20px;
            }
            
            .header h1 {
                font-size: 2em;
            }
            
            .content {
                padding: 20px;
            }
            
            .search-item {
                flex-direction: column;
                align-items: flex-start;
            }
            
            .search-item .btn {
                margin-top: 15px;
                align-self: flex-end;
            }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>🔍 Пошук оголошень OLX</h1>
            <p>Автоматичний моніторинг нових оголошень з нотифікаціями в Telegram</p>
        </div>
        
        <div class="content">
            <div class="info-box">
                <h3>📋 Як це працює:</h3>
                <ul>
                    <li>Додайте посилання на пошук OLX з унікальною назвою</li>
                    <li>Система перевіряє пошуки за розкладом і надсилає лише нові оголошення</li>
                    <li>Нотифікації в Telegram за розкладом: {{.Schedule}}</li>
                    <li>Фільтруємо рекламні оголошення та показуємо тільки реальні пропозиції</li>
                </ul>
            </div>
            
            <div class="form-section">
                <h2>➕ Додати новий пошук</h2>
                <form method="POST" action="/add">
                    <div class="form-group">
                        <label for="name">Назва пошуку:</label>
                        <input type="text" id="name" name="name" placeholder="Наприклад: Dr. Martens 1460" required>
                    </div>
                    <div class="form-group">
                        <label for="url">Посилання на пошук OLX:</label>
                        <input type="text" id="url" name="url" placeholder="https://www.olx.ua/uk/list/q-dr-martens-1460/" required>
                    </div>
                    <button type="submit" class="btn">Додати пошук</button>
                </form>
            </div>
            
            <div class="searches-section">
                <h2>📋 Активні пошуки</h2>
                {{if .Orphans}}
                <div class="search-item">
                    <div class="search-info">
                        <h3>🗂️ Старі записи без прив'язки: {{.Orphans}}</h3>
                        <p>Зібрані до появи зв'язку «оголошення → пошук». Працюють лише на дедуплікацію, переглянути їх не можна.</p>
                    </div>
                    <div class="search-actions">
                        <form method="POST" action="/clear-orphans" style="display: inline; width: 100%;">
                            <button type="submit" class="btn btn-danger" onclick="return confirm('Видалити {{.Orphans}} старих записів без прив\'язки? Дію не скасувати.')">🧹 Видалити {{.Orphans}} записів</button>
                        </form>
                    </div>
                </div>
                {{end}}
                {{if .Searches}}
                    {{range .Searches}}
                    <div class="search-item">
                        <div class="search-info">
                            <h3>{{.Name}}</h3>
                            <p>{{.URL}}</p>
                        </div>
                        <div class="search-actions">
                            <a href="/listings?id={{.ID}}" class="btn">👁️ Оголошення ({{.Listings}})</a>
                            <a href="/edit?id={{.ID}}" class="btn btn-info">✏️ Редагувати</a>
                            <form method="POST" action="/clear" style="display: inline; width: 100%;">
                                <input type="hidden" name="id" value="{{.ID}}">
                                <button type="submit" class="btn btn-warning" onclick="return confirm('Очистити базу оголошень для цього пошуку?')">🧹 Очистити базу</button>
                            </form>
                            {{if .Active}}
                            <form method="POST" action="/deactivate" style="display: inline; width: 100%;">
                                <input type="hidden" name="id" value="{{.ID}}">
                                <button type="submit" class="btn btn-secondary" onclick="return confirm('Відключити цей пошук?')">⏸️ Відключити</button>
                            </form>
                            {{else}}
                            <form method="POST" action="/activate" style="display: inline; width: 100%;">
                                <input type="hidden" name="id" value="{{.ID}}">
                                <button type="submit" class="btn btn-success">▶️ Увімкнути</button>
                            </form>
                            {{end}}
                            <form method="POST" action="/delete" style="display: inline; width: 100%;">
                                <input type="hidden" name="id" value="{{.ID}}">
                                <button type="submit" class="btn btn-danger" onclick="return confirm('Видалити цей пошук?')">🗑️ Видалити</button>
                            </form>
                        </div>
                    </div>
                    {{end}}
                {{else}}
                    <div class="empty-state">
                        <p>📭 Поки що немає активних пошуків</p>
                        <p>Додайте свій перший пошук вище, щоб почати отримувати нотифікації!</p>
                    </div>
                {{end}}
            </div>
        </div>
    </div>
</body>
</html>`

const editTemplate = `
<!DOCTYPE html>
<html lang="uk">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Редагування пошуку</title>
    <style>
        body {
            font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            padding: 20px;
            margin: 0;
        }
        .container {
            max-width: 600px;
            margin: 0 auto;
            background: white;
            border-radius: 15px;
            box-shadow: 0 20px 40px rgba(0,0,0,0.1);
            overflow: hidden;
        }
        .header {
            background: linear-gradient(135deg, #17a2b8 0%, #138496 100%);
            color: white;
            padding: 30px;
            text-align: center;
        }
        .header h1 {
            font-size: 2em;
            margin: 0;
        }
        .content {
            padding: 30px;
        }
        .form-group {
            margin-bottom: 20px;
        }
        .form-group label {
            display: block;
            margin-bottom: 8px;
            font-weight: 600;
            color: #555;
        }
        .form-group input[type="text"] {
            width: 100%;
            padding: 12px 15px;
            border: 2px solid #e1e5e9;
            border-radius: 8px;
            font-size: 16px;
            transition: border-color 0.3s ease;
        }
        .form-group input[type="text"]:focus {
            outline: none;
            border-color: #17a2b8;
            box-shadow: 0 0 0 3px rgba(23, 162, 184, 0.1);
        }
        .btn {
            background: linear-gradient(135deg, #17a2b8 0%, #138496 100%);
            color: white;
            border: none;
            padding: 12px 25px;
            border-radius: 8px;
            font-size: 16px;
            font-weight: 600;
            cursor: pointer;
            transition: transform 0.2s ease, box-shadow 0.2s ease;
            margin-right: 10px;
        }
        .btn:hover {
            transform: translateY(-2px);
            box-shadow: 0 5px 15px rgba(23, 162, 184, 0.3);
        }
        .btn-secondary {
            background: linear-gradient(135deg, #6c757d 0%, #545b62 100%);
        }
        .btn-secondary:hover {
            box-shadow: 0 5px 15px rgba(108, 117, 125, 0.3);
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>✏️ Редагування пошуку</h1>
        </div>
        <div class="content">
            <form method="POST" action="/edit">
                <input type="hidden" name="id" value="{{.ID}}">
                <div class="form-group">
                    <label for="name">Назва пошуку:</label>
                    <input type="text" id="name" name="name" value="{{.Name}}" required>
                </div>
                <div class="form-group">
                    <label for="url">Посилання на пошук OLX:</label>
                    <input type="text" id="url" name="url" value="{{.URL}}" required>
                </div>
                <button type="submit" class="btn">💾 Зберегти зміни</button>
                <a href="/" class="btn btn-secondary">❌ Скасувати</a>
            </form>
        </div>
    </div>
</body>
</html>`

const listingsTemplate = `
<!DOCTYPE html>
<html lang="uk">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Search.Name}} — зібрані оголошення</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            padding: 20px;
        }
        .container {
            max-width: 900px; margin: 0 auto; background: white;
            border-radius: 15px; box-shadow: 0 20px 40px rgba(0,0,0,0.1); overflow: hidden;
        }
        .header {
            background: linear-gradient(135deg, #0198d0 0%, #0077b3 100%);
            color: white; padding: 25px 30px;
        }
        .header h1 { font-size: 1.8em; font-weight: 300; margin-bottom: 8px; }
        .header a { color: #d6f1ff; word-break: break-all; font-size: 0.9em; }
        .meta { padding: 15px 30px; background: #f8f9fa; border-bottom: 1px solid #e1e5e9;
                display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 10px; }
        .meta strong { color: #0198d0; font-size: 1.1em; }
        .content { padding: 20px 30px 30px; }
        .listing {
            border: 1px solid #e1e5e9; border-radius: 10px; padding: 15px;
            margin-bottom: 12px; transition: border-color .2s ease, transform .2s ease;
        }
        .listing:hover { border-color: #0198d0; transform: translateY(-2px); }
        .listing h3 { font-size: 1.05em; margin-bottom: 8px; }
        .listing h3 a { color: #333; text-decoration: none; }
        .listing h3 a:hover { color: #0198d0; }
        .tags { display: flex; gap: 15px; flex-wrap: wrap; color: #666; font-size: .9em; }
        .price { color: #28a745; font-weight: 600; }
        .btn {
            display: inline-block; background: linear-gradient(135deg, #0198d0 0%, #0077b3 100%);
            color: white; border: none; padding: 10px 20px; border-radius: 8px;
            font-size: 15px; font-weight: 600; cursor: pointer; text-decoration: none;
        }
        .btn-secondary { background: linear-gradient(135deg, #6c757d 0%, #545b62 100%); }
        .pager { display: flex; justify-content: space-between; align-items: center; margin-top: 20px; gap: 10px; }
        .empty { text-align: center; padding: 40px 20px; color: #666; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>👁️ {{.Search.Name}}</h1>
            <a href="{{.Search.URL}}" target="_blank" rel="noopener">{{.Search.URL}}</a>
        </div>

        <div class="meta">
            <div>Зібрано оголошень: <strong>{{.Total}}</strong>{{if gt .Pages 1}} · сторінка {{.Page}} з {{.Pages}}{{end}}</div>
            <a href="/" class="btn btn-secondary">← До списку пошуків</a>
        </div>

        <div class="content">
            {{if .Listings}}
                {{range .Listings}}
                <div class="listing">
                    <h3><a href="{{.URL}}" target="_blank" rel="noopener">{{.Title}}</a></h3>
                    <div class="tags">
                        {{if .Price}}<span class="price">💰 {{.Price}}</span>{{end}}
                        {{if .Location}}<span>📍 {{.Location}}</span>{{end}}
                        <span>🕓 знайдено {{.FirstSeen}}</span>
                    </div>
                </div>
                {{end}}

                {{if gt .Pages 1}}
                <div class="pager">
                    {{if .HasPrev}}<a href="/listings?id={{$.Search.ID}}&page={{.PrevPage}}" class="btn">← Новіші</a>{{else}}<span></span>{{end}}
                    {{if .HasNext}}<a href="/listings?id={{$.Search.ID}}&page={{.NextPage}}" class="btn">Старіші →</a>{{else}}<span></span>{{end}}
                </div>
                {{end}}
            {{else}}
                <div class="empty">
                    <p>📭 Для цього пошуку ще нічого не зібрано.</p>
                    <p>Оголошення з'являться тут після найближчої перевірки.</p>
                </div>
            {{end}}
        </div>
    </div>
</body>
</html>`
