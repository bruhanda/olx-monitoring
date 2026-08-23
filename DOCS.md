# Info Parser — технічна документація

Сервіс на Go, який періодично парсить сторінки пошуку OLX, зберігає знайдені
оголошення в SQLite і надсилає в Telegram **лише нові** позиції. Список пошуків
керується через вбудовану веб-форму або через змінні оточення.

- Модуль: `github.com/bruhanda/olx-monitoring`
- Go: 1.23+ (toolchain 1.24.6)
- Залежності: `goquery` (HTML-парсинг), `gorm` + `driver/sqlite` (ORM), `godotenv` (.env)

---

## 1. Огляд архітектури

```
                      ┌───────────────────────┐
   .env / env vars ──▶│  internal/config      │  Load() → Config
                      └───────────┬───────────┘
                                  │
                      ┌───────────▼───────────┐
                      │  cmd/info-parser      │  main(): збирає все докупи
                      └──┬────────┬────────┬──┘
                         │        │        │
         ┌───────────────▼──┐  ┌──▼─────┐ ┌▼──────────────────┐
         │ internal/storage │  │ http   │ │ internal/scheduler│
         │ GORM + SQLite    │◀─│ server │ │  цикл за розкладом│
         │ Listing          │  │ :8088  │ │  JobProvider ─────┼─┐
         │ SavedSearch      │  │ CRUD   │ └──┬────────────┬───┘ │
         └──────────────────┘  └────────┘    │            │     │
                    ▲                        │            │     │
                    │              ┌─────────▼──┐  ┌──────▼─────▼────┐
                    └──────────────│ parser.    │  │ notifier.       │
                     активні пошуки│ Parser     │  │ Telegram        │
                     перед кожним  │  └─ olx    │  │  sendMessage    │
                     запуском      └────────────┘  └─────────────────┘
```

Потік роботи:

1. `config.Load()` читає `.env` + змінні оточення, валідує обов'язкові поля
   (падає з `log.Fatal`, якщо їх немає або вони суперечливі).
2. `storage.Open()` відкриває SQLite і виконує `AutoMigrate` для двох таблиць.
3. Пошуки з `OLX_SEARCH_URLS` / `OLX_SEARCH_URLS_WITH_NAMES` засіваються в БД
   (ідемпотентно, за унікальним URL).
4. У окремій горутині стартує HTTP-сервер адмінки (з опційним basic auth).
5. `scheduler.Run()` блокує main-горутину: чекає до наступного часу з
   `DAILY_NOTIFY_TIMES` (або тікає за інтервалом, якщо часи не задані).
   **Перед кожним запуском** він викликає `JobProvider`, який заново читає
   активні пошуки з БД і будує парсери — рестарт після змін в адмінці не
   потрібен.
6. `SIGINT`/`SIGTERM` закривають канал `stop` → планувальник завершується.

---

## 2. Пакети

### `cmd/info-parser` — точка входу

`main.go` створює каталог під файл БД, ініціалізує сховище, нотифаєр,
HTTP-сервер, оголошує `JobProvider` і запускає планувальник. Обробка сигналів —
через `signal.Notify` + канал `stop`.

### `internal/config` — конфігурація

`Config` (усі поля читаються один раз на старті):

| Поле | Env | Дефолт | Опис |
|---|---|---|---|
| `TelegramBotToken` | `TELEGRAM_BOT_TOKEN` | — (обов'язкова) | токен бота |
| `TelegramChatID` | `TELEGRAM_CHAT_ID` | — (обов'язкова) | id чату, `int64` |
| `DatabasePath` | `DATABASE_PATH` | `./data/info-parser.db` | файл SQLite |
| `PollInterval` | `POLL_INTERVAL_SEC` | `60` | інтервал fallback-режиму |
| `OlxSearchURLs` | `OLX_SEARCH_URLS` | — | URL через кому/пробіл/новий рядок |
| `SeedPairs` | `OLX_SEARCH_URLS_WITH_NAMES` | — | рядки `Назва\|URL`, по одному на рядок |
| `RequestTimeout` | `REQUEST_TIMEOUT_SEC` | `15` | таймаут HTTP-клієнта |
| `RequestDelay` | `REQUEST_DELAY_SEC` | `3` | базова пауза між пошуками |
| `RequestJitter` | `REQUEST_JITTER_SEC` | `2` | випадкова добавка `0..jitter` |
| `UserAgent` | `HTTP_USER_AGENT` | Chrome 120 UA | передається в запити OLX |
| `HTTPAddr` | `HTTP_ADDR` | `:8088` | адреса адмінки |
| `HTTPUser` | `HTTP_BASIC_AUTH_USER` | — | basic auth, порожнє = без авторизації |
| `HTTPPassword` | `HTTP_BASIC_AUTH_PASS` | — | має задаватись разом з користувачем |
| `MaxItemsPerRun` | `MAX_ITEMS_PER_RUN` | `10` | максимум нових оголошень на пошук за запуск |
| `NotifyTimes` | `DAILY_NOTIFY_TIMES` | `11:00,15:00,20:00` | час доби `HH:MM`, некоректні значення відкидаються |

Окремо: `OLX_DEBUG=1` вмикає докладні логи парсера (читається безпосередньо
пакетом `internal/parser/olx`).

Якщо задано лише один з `HTTP_BASIC_AUTH_USER` / `HTTP_BASIC_AUTH_PASS` —
процес падає на старті. Якщо не задано жодного — у лог пишеться попередження
про незахищену адмінку.

### `internal/storage` — сховище

Дві GORM-моделі:

```go
type Listing struct {                  // знайдене оголошення
    ID, CreatedAt, UpdatedAt
    SearchID   uint    // власник (SavedSearch.ID); 0 у записів до міграції
    Source     string  // "olx"        ─┐ складений унікальний
    ExternalID string  // sha1(URL)    ─┘ індекс idx_source_external
    Title, URL, Price, Location string
    PostedAt   time.Time
}

type SavedSearch struct {              // збережений пошук
    ID, CreatedAt, UpdatedAt
    Name   string
    URL    string  // uniqueIndex
    Active bool    // default true
    Source string  // default "olx"
}
```

API `*Store`:

| Метод | Призначення |
|---|---|
| `Open(path) *Store` | відкрити БД + `AutoMigrate` (падає при помилці) |
| `CreateListingIfNotExists(*Listing) (bool, error)` | вставка з дедуплікацією за `(source, external_id)`; `true` = оголошення нове |
| `CreateSearchIfNotExists(name, url, source, active) (bool, error)` | вставка пошуку за унікальним `url` |
| `ListActiveSearches(source)` / `ListAllSearches(source)` | вибірка пошуків, сортування за `id` |
| `GetSearchByID(id)` | один пошук; `ErrSearchNotFound`, якщо немає |
| `ActivateSearch(id)` / `DeactivateSearch(id)` | перемикання прапорця `Active` |
| `UpdateSearch(id, name, url)` | редагування |
| `DeleteSearchByID(id)` | видалення пошуку **разом з його оголошеннями** (у транзакції) |
| `ClearListingsForSearchID(id)` | видалити оголошення пошуку → наступний запуск знову вважатиме їх новими |

Логер GORM вимкнено (`logger.Silent`).

### `internal/parser` — контракт джерела

```go
type Item struct {
    ExternalID, Title, URL, Price, Location string
    PostedAt time.Time
}

type Parser interface {
    Source() string          // ідентифікатор джерела, напр. "olx"
    Label() string           // людська назва пошуку
    URL() string             // URL сторінки пошуку
    Fetch() ([]Item, error)  // одна вибірка
}
```

### `internal/parser/olx` — парсер OLX

`New(client, ua, searchURL)` / `NewWithName(client, ua, searchURL, name)`.
Порожній `ua` замінюється на `defaultUserAgent` (Chrome 120): OLX віддає порожню
сторінку явно небраузерним клієнтам.

`Fetch()` робить `GET` на URL пошуку з `User-Agent` з конфігурації та
заголовками `Accept-Language: uk-UA`, вимагає статус 200, віддає тіло в
`goquery`. Кожен запуск логує один рядок підсумку; детальні логи (включно з HTML
кожної картки) вмикаються `OLX_DEBUG=1`.

`parseHTML()`:
- `getRealCount()` перебирає селектори (`[data-testid='total-count']` та
  запасні) і витягує з тексту цифри — це «скільки оголошень показує OLX»;
- якщо count = 0 і на сторінці є текст на кшталт «Ми знайшли 0 оголошень» —
  повертає порожній список;
- обходить картки `[data-testid='l-card']`, з кожної бере:
  - **URL** — перше посилання `a[href*='/d/uk/obyavlenie/']`, відносний шлях
    доповнюється до `https://www.olx.ua`;
  - **Title** — текст того ж посилання, прогнаний через `cleanText()`
    (CSS-сміття перетворюється на порожній рядок → картка пропускається);
  - **Price** — `[data-testid='ad-price']`;
  - **Location** — евристика: перший короткий текст (<100 символів) із комою або
    словами `р.`, `Сьогодні`, `Київ`, `Львів`, `Харків`;
- відкидає картки без URL/заголовка та «альтернативні» результати
  (`isAlternativeListing`);
- `ExternalID = sha1(href)`, `PostedAt = time.Now()` (реальна дата публікації
  **не парситься**).

`isAlternativeListing()` відсіює підказки OLX «схожі оголошення»: за маркером
`reason=extended_search…` в URL або за тим, що заголовок не містить жодного
ключового слова з сегмента `q-…` вихідного URL пошуку.

### `internal/notifier` — Telegram

`NewTelegram(token, chatID, timeout)` → `SendMessage(text) error`: `POST` на
`https://api.telegram.org/bot<token>/sendMessage` з JSON
`{chat_id, text, parse_mode:"HTML", disable_web_page_preview:true}`.

- статус 2xx → `nil`;
- статус 429 → одна повторна спроба після `parameters.retry_after`
  (або заголовка `Retry-After`), не довше 30 с;
- інші не-2xx → помилка з кодом і тілом відповіді (у ній Telegram пише причину,
  напр. `can't parse entities`).

### `internal/scheduler` — планувальник і формування звітів

```go
scheduler.New(store, tg, interval, jobProvider).
    WithDelays(baseDelay, jitter).
    WithLimit(maxItemsPerRun).
    AtTimes([]string{"11:00","15:00","20:00"})
```

`JobProvider func() ([]Job, error)` повертає пари «збережений пошук + парсер» і
викликається перед кожним запуском.

`Run(stop)`:
- якщо `times` порожній — `time.Ticker` з `PollInterval`, перший запуск одразу;
- інакше — цикл «поспати до `nextOccurrence()` → `runOnce()`»; час наступного
  запуску пишеться в лог. `nextOccurrence` бере найближчий час зі списку
  сьогодні, інакше перший час завтра (локальна таймзона процесу).

`runOnce()` завантажує джоби, послідовно виконує кожну (з паузою
`baseDelay + rand(0..jitter)` між ними) і надсилає підсумкове повідомлення.

`runJob()` для одного пошуку:
1. `Fetch()`; помилка потрапляє в підсумок запуску, обхід не переривається.
2. Відкидає рекламу через `isAdItem()`.
3. Кожне нерекламне оголошення пише в БД через `CreateListingIfNotExists`;
   у список на відправку потрапляють **лише ті, що створені вперше**.
4. Якщо нових немає — жодного повідомлення (тиша замість спаму).
5. Якщо є — заголовок (джерело, назва, URL, всього / не рекламних / нових),
   до `MAX_ITEMS_PER_RUN` карток і роздільник.

Підсумок запуску (`runSummary`) — одне повідомлення:

```
🔄 Перевірка 20.08 15:00
Пошуків: 13
Нових оголошень: 4
Помилок: 1
• Черевики Dr. Martens: unexpected status: 403
```

Формат картки (`renderWithNameUA`):

```
📌 [olx] Назва пошуку
🏷️ Назва: <заголовок>
💰 Ціна: <ціна>
📍 Локація: <локація>
🔗 Посилання: <url>
```

Усі підставлені значення проходять через `html.EscapeString` — Telegram працює
в режимі `parse_mode: HTML`, тож `&`, `<`, `>` у заголовках мають бути
екрановані.

`isAdItem()` — евристичний фільтр за заголовком/ціною: маркери реклами
(`топ`, `реклама`, `преміум`, `sponsored`, `підняти`, `виділити`, `популярне`)
плюс «підозрілі» слова (`безкоштовно`, `віддам`, `обмін`, `робота`, `вакансія`,
`заробіток`, `бізнес` тощо) і надто короткі/CSS-подібні заголовки.

### `internal/httpserver` — веб-адмінка

`New(store, Options{NotifyTimes, Username, Password})`. `Handler()` будує
власний `http.ServeMux` (не `DefaultServeMux`) і загортає його в middleware
basic auth, якщо задано облікові дані (порівняння —
`subtle.ConstantTimeCompare`). `Start(addr)` піднімає `http.Server` з
таймаутами читання/запису.

Обидві сторінки — шаблони `html/template`, розібрані один раз на старті
(`template.Must`), тому будь-які дані з БД автоматично екрануються.

| Маршрут | Метод | Дія |
|---|---|---|
| `/` | GET | список усіх пошуків + форма додавання + актуальний розклад |
| `/add` | POST | `name`, `url` → `CreateSearchIfNotExists` (URL має бути `http(s)://`) |
| `/edit` | GET | форма редагування; єдиний параметр — `id`, значення беруться з БД |
| `/edit` | POST | `id`, `name`, `url` → `UpdateSearch` |
| `/delete` | POST | `id` → пошук разом з оголошеннями |
| `/deactivate` / `/activate` | POST | `id` → перемикання `Active` |
| `/clear` | POST | `id` → `ClearListingsForSearchID` |
| `/healthz` | GET | `200 ok`, в обхід basic auth — для проксі та моніторингу |

Усі мутуючі хендлери приймають лише `POST` і завершуються редіректом
`303 See Other` на `/`.

---

## 3. Запуск

```bash
go mod tidy
go build ./cmd/info-parser
./info-parser
```

Конфігурація — `cp .env.example .env` і заповнити значення. Мінімум:

```env
TELEGRAM_BOT_TOKEN=123456:ABC...
TELEGRAM_CHAT_ID=-1001234567890
HTTP_BASIC_AUTH_USER=admin
HTTP_BASIC_AUTH_PASS=<пароль>
```

Після старту адмінка доступна на `http://localhost:8088`.

Тести й перевірки:

```bash
go test ./...
go vet ./...
gofmt -l ./cmd ./internal   # має бути порожньо
```

Приклад unit-файла systemd:

```ini
[Unit]
Description=Info Parser
After=network-online.target

[Service]
WorkingDirectory=/opt/olx-monitoring
ExecStart=/opt/olx-monitoring/info-parser
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```

`WorkingDirectory` важливий: шляхи `./data` і `.env` — відносні.

---

## 4. Життєвий цикл даних

- Оголошення ідентифікується як `sha1(URL оголошення)` в парі з `source`.
  Повторна вставка того самого оголошення — no-op.
- `PostedAt` = момент парсингу, а не публікації на OLX.
- Дедуплікація визначає, що піде в Telegram: надсилаються лише щойно створені
  записи, не більше `MAX_ITEMS_PER_RUN` на пошук за запуск.
- Щоб примусово «забути» пошук і отримати його результати як нові —
  кнопка «🧹 Очистити базу» в адмінці.
- **Міграція `search_id`.** Записи, створені до появи поля, мають `search_id = 0`.
  Колонка додається автоматично (`AutoMigrate`), а старі записи отримують
  прив'язку до пошуку, коли те саме оголошення трапиться при наступному
  парсингу. Ці записи вже вважаються «баченими», тож повторних нотифікацій не
  буде; кнопка очищення для них спрацює лише після такого бекфілу.

---

## 5. Як додати нове джерело

1. Створити пакет `internal/parser/<source>`.
2. Реалізувати `parser.Parser`: `Source()`, `Label()`, `URL()`, `Fetch()`.
   `Fetch()` має повертати `[]parser.Item` зі стабільним `ExternalID`
   (наприклад, `sha1` канонічного URL).
3. У `cmd/info-parser/main.go` розширити `JobProvider`: прочитати
   `store.ListActiveSearches("<source>")` і додати відповідні `scheduler.Job`.
4. За потреби розширити адмінку: зараз усі запити до сховища жорстко
   передають `"olx"` як `source`.

---

## 6. Обмеження, про які варто знати

Актуальний список після рефакторингу (історію виправлених дефектів див. у §7):

1. **Парсинг залежить від верстки OLX** (`data-testid` атрибути) та від
   антибот-захисту. Будь-який не-200 статус фіксується як помилка пошуку і
   потрапляє в підсумкове повідомлення запуску.
2. **Локація визначається евристично** (кома / назви кількох міст) — для інших
   міст поле може лишатися порожнім.
3. **`PostedAt` не відображає реальну дату публікації** на OLX.
4. **Фільтр реклами — за списком слів.** Легально «віддам»/«обмін» у заголовку
   теж відсіється; список у `isAdItem` доведеться підтримувати.
5. **Пагінація не підтримується** — обробляється лише перша сторінка пошуку.
6. **Один Telegram-чат на весь сервіс** (`TELEGRAM_CHAT_ID`), без маршрутизації
   пошуків по чатах.
7. **Адмінка без TLS.** Basic auth захищає від випадкового доступу, але пароль
   іде відкритим текстом — за межами локальної мережі потрібен реверс-проксі
   з HTTPS.
8. **`.env` містить робочий токен бота.** Файл додано в `.gitignore`; якщо він
   колись потрапляв у спільний доступ — токен варто перевипустити через
   @BotFather.

---

## 7. Що було виправлено

Дефекти, знайдені при написанні документації, і як вони закриті:

| Проблема | Виправлення |
|---|---|
| `escape()` зациклювався на будь-якому `&` (власна реалізація `replaceAll` знаходила `&` у щойно вставленому `&amp;`) — горутина планувальника підвисала | `html.EscapeString`; регресійний тест `TestEscapeTerminatesOnAmpersand` |
| Нотифікації надсилались щоразу для перших 5 карток, дедуплікація в БД не впливала на них | надсилаються лише нові записи, ліміт `MAX_ITEMS_PER_RUN`, підсумок запуску одним повідомленням |
| Список парсерів фіксувався на старті — новий пошук вимагав рестарту | `JobProvider` перечитує активні пошуки перед кожним запуском |
| «Очистити базу» нічого не чистила (`url = <URL пошуку>` не збігався з URL оголошень) | у `Listing` додано `SearchID`; `ClearListingsForSearchID`, видалення пошуку прибирає його оголошення в транзакції |
| `HTTP_USER_AGENT` ігнорувався OLX-парсером | UA береться з конфігурації, дефолт — лише як fallback; тест `TestFetchUsesConfiguredUserAgent` |
| XSS у GET `/edit` (`fmt.Sprintf` по query-параметрах) | `html/template` + завантаження значень з БД за `id`; тести на екранування та ігнорування query |
| Адмінка без автентифікації, на глобальному `DefaultServeMux`, без таймаутів | окремий `ServeMux`, опційний basic auth зі стійким порівнянням, таймаути `http.Server` |
| Не було тестів і CI | 25+ тестів у чотирьох пакетах + GitHub Actions (`gofmt`, `vet`, `test`, `build`) |
| Логи друкували HTML кожної картки | тихо за замовчуванням, детально — під `OLX_DEBUG=1` |
| Помилки Telegram без деталей, без обробки rate limit | тіло відповіді в тексті помилки, повтор на 429 |
| Код не відформатований `gofmt`, `.gitignore` відсутній | `gofmt` по всьому проєкту, `.gitignore` + `.env.example` |
