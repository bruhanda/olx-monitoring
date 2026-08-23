# OLX Monitoring

[![CI](https://github.com/bruhanda/olx-monitoring/actions/workflows/ci.yml/badge.svg)](https://github.com/bruhanda/olx-monitoring/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.23%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![SQLite](https://img.shields.io/badge/storage-SQLite-003B57?logo=sqlite&logoColor=white)](https://www.sqlite.org)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

Сервіс на Go, який стежить за збереженими пошуками на OLX і надсилає в Telegram
**лише нові** оголошення. Пошуки додаються через веб-форму, дедуплікація — у
SQLite, розклад перевірок налаштовується під себе.

> Робить те саме, що ручне оновлення сторінки OLX по десять разів на день —
> тільки без вас.

---

## Що вміє

- 🔍 **Багато пошуків одночасно** — кожен зі своєю назвою, вмикається й
  вимикається окремо.
- 🆕 **Тільки нові оголошення** — усе побачене раніше зберігається в SQLite і
  вдруге не надсилається.
- 🌐 **Веб-адмінка** — додати, перейменувати, вимкнути чи видалити пошук без
  рестарту сервісу; опційний basic auth.
- ⏰ **Розклад під себе** — кілька разів на добу в заданий час або постійне
  опитування з інтервалом.
- 🧹 **Фільтр реклами** — «ТОП», «преміум», вакансії та інший шум відсіюються
  до відправки.
- 📨 **Акуратні нотифікації** — картка з назвою, ціною, локацією та посиланням
  плюс один підсумок за запуск.
- 🧩 **Розширюваність** — джерело описується одним інтерфейсом на чотири методи.

## Як це працює

```
   веб-форма ──▶ SavedSearch (SQLite)
                      │
                      ▼
   планувальник ──▶ парсер OLX ──▶ дедуплікація ──▶ Telegram
   (11:00/15:00/20:00)   goquery      sha1(URL)      лише нове
```

Перед кожним запуском планувальник перечитує активні пошуки з бази, тож зміни
в адмінці застосовуються одразу. Кожне оголошення ідентифікується за
`sha1(URL)`, тому повторів у чаті не буде.

## Швидкий старт

```bash
git clone git@github.com:bruhanda/olx-monitoring.git
cd olx-monitoring
cp .env.example .env      # вкажіть токен бота і chat id
go build ./cmd/info-parser
./info-parser
```

Далі відкрийте `http://localhost:8088`, додайте посилання на пошук OLX
(наприклад `https://www.olx.ua/uk/list/q-dr-martens-1460/`) — і чекайте
на нотифікації.

Потрібен Go 1.23+ (для SQLite-драйвера потрібен cgo, тобто наявний gcc).

## Приклад нотифікації

```
🔍 olx [Черевики Dr. Martens]
Посилання: https://www.olx.ua/uk/list/q-dr-martens-1460/
Всього оголошень: 34
Не рекламних: 28
Нових: 2

📌 [olx] Черевики Dr. Martens
🏷️ Назва: Черевики Dr. Martens 1460, розмір 42
💰 Ціна: 4 200 грн
📍 Локація: Київ, Печерський
🔗 Посилання: https://www.olx.ua/d/uk/obyavlenie/...

🔄 Перевірка 20.08 15:00
Пошуків: 13
Нових оголошень: 2
```

## Конфігурація

Усі налаштування — через `.env` або змінні оточення (див. `.env.example`).

| Змінна | Дефолт | Опис |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | — | токен Telegram-бота (обов'язково) |
| `TELEGRAM_CHAT_ID` | — | id чату-отримувача (обов'язково) |
| `DATABASE_PATH` | `./data/info-parser.db` | файл SQLite |
| `HTTP_ADDR` | `:8088` | адреса веб-адмінки |
| `HTTP_BASIC_AUTH_USER` / `HTTP_BASIC_AUTH_PASS` | — | авторизація адмінки; задавати разом, порожні = без захисту |
| `DAILY_NOTIFY_TIMES` | `11:00,15:00,20:00` | час доби для перевірок, `HH:MM` через кому |
| `POLL_INTERVAL_SEC` | `60` | інтервал опитування, якщо `DAILY_NOTIFY_TIMES` порожній |
| `MAX_ITEMS_PER_RUN` | `10` | максимум нових оголошень на пошук за один запуск |
| `REQUEST_TIMEOUT_SEC` | `15` | таймаут HTTP-запитів |
| `REQUEST_DELAY_SEC` | `3` | базова пауза між пошуками |
| `REQUEST_JITTER_SEC` | `2` | випадкова добавка до паузи, `0..jitter` |
| `HTTP_USER_AGENT` | Chrome 120 UA | User-Agent для запитів до OLX |
| `OLX_SEARCH_URLS` | — | URL пошуків для засівання БД, через кому/пробіл/новий рядок |
| `OLX_SEARCH_URLS_WITH_NAMES` | — | рядки `Назва\|URL`, по одному на рядок |
| `OLX_DEBUG` | — | `1` — докладні логи парсера, включно з HTML карток |

## Веб-адмінка

`/` — список пошуків, форма додавання і поточний розклад. Операції:
`/add`, `/edit`, `/delete`, `/activate`, `/deactivate`, `/clear` (забути
зібрані оголошення пошуку, щоб отримати їх як нові).

Сервіс працює без TLS — назовні виставляйте лише через реверс-проксі з HTTPS
і обов'язково вмикайте basic auth.

## Структура проєкту

```
cmd/info-parser        точка входу, збирання залежностей
internal/config        читання .env і змінних оточення
internal/storage       GORM + SQLite: Listing, SavedSearch
internal/parser        інтерфейс Parser і нормалізований Item
internal/parser/olx    парсер сторінок пошуку OLX (goquery)
internal/scheduler     розклад, дедуплікація, формування повідомлень
internal/notifier      клієнт Telegram Bot API
internal/httpserver    веб-адмінка (html/template, basic auth)
```

## Розробка

```bash
go test ./...
go vet ./...
gofmt -l ./cmd ./internal   # має бути порожньо
```

## Додати нове джерело

Реалізуйте інтерфейс `internal/parser.Parser` (`Source`, `Label`, `URL`,
`Fetch`) і додайте його в `JobProvider` у `cmd/info-parser/main.go`.
Покроково — у [DOCS.md](DOCS.md#5-як-додати-нове-джерело).

## Деплой

Готовий Docker-стек під Traefik: `Dockerfile`, `docker/docker-compose.prod.yml`
і покрокова інструкція — [DEPLOY.md](DEPLOY.md). Health-check без авторизації:
`GET /healthz`.

## Документація

Архітектура, опис кожного пакета, життєвий цикл даних, відомі обмеження та
історія виправлених дефектів — [DOCS.md](DOCS.md).

## Ліцензія

[MIT](LICENSE) © bruhanda
