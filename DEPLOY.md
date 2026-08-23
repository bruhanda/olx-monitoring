# Деплой на Hetzner CPX22 (188.245.71.3)

Сервіс їде поруч із рештою проєктів за спільним Traefik. Загальні правила
сервера — у `~/WEB/SERVER_SETUP.md` на локальній машині власника.

- Домен: **olx.kozelets.pp.ua**
- Каталог на сервері: `~/server/olx.kozelets.pp.ua/`
- Compose project name: **olxmon** (обов'язковий `name:`, див. §4.1 SERVER_SETUP)
- Том з базою: `olxmon_olxmon_data` → `/data/info-parser.db`

---

## 1. DNS (робиться першим!)

У реєстратора (nic.ua, NS `ns10-12.uadns.com`) додати запис:

```
olx.kozelets.pp.ua.   A   188.245.71.3
```

Перевірити, що розійшлося:

```bash
dig +short @8.8.8.8 olx.kozelets.pp.ua      # має бути 188.245.71.3
```

⚠️ Піднімати стек **до** появи DNS не можна: Let's Encrypt отримає помилку
валідації, а повторні спроби йдуть з наростаючою затримкою.

## 2. Завантажити код на сервер

З локальної машини:

```bash
ssh deploy@188.245.71.3 'mkdir -p ~/server/olx.kozelets.pp.ua'

rsync -avz --delete \
  --exclude=.git --exclude=/data --exclude=/.env --exclude=/.env.production \
  --exclude=/info-parser --exclude=.idea --exclude=.vscode \
  ~/go/src/info-parser/ deploy@188.245.71.3:/home/deploy/server/olx.kozelets.pp.ua/
```

⚠️ **Слеш перед іменем обов'язковий.** `--exclude=info-parser` без нього збігається
з будь-яким компонентом шляху — зокрема з каталогом `cmd/info-parser/`, і збірка
падає з `stat /src/cmd/info-parser: directory not found`.

## 3. Секрети

```bash
ssh deploy@188.245.71.3
cd ~/server/olx.kozelets.pp.ua
cp .env.production.example .env.production
nano .env.production          # токен бота, chat id, пароль адмінки
chmod 600 .env.production
```

Пароль адмінки згенерувати: `openssl rand -base64 24`.

## 4. Підняти

```bash
cd ~/server/olx.kozelets.pp.ua/docker
docker compose -f docker-compose.prod.yml up -d --build
```

⚠️ **Ніколи не додавати `--remove-orphans`** — на цьому сервері воно вже
зносило чужі проєкти.

## 5. Перевірити

```bash
docker compose -f docker-compose.prod.yml ps
docker logs -f olxmon_app          # має бути "HTTP server starting ... (basic auth enabled)"
                                   # і "scheduler: next run at ..."
curl -s -o /dev/null -w '%{http_code}\n' https://olx.kozelets.pp.ua/healthz   # 200
curl -s -o /dev/null -w '%{http_code}\n' https://olx.kozelets.pp.ua/          # 401 без логіна
```

Сертифікат Let's Encrypt Traefik візьме сам за 10–30 секунд після старту.

Рядок `Warning: .env file not found` у логах — очікуваний: конфігурація
приходить із `env_file`, а не з файла `.env` усередині контейнера.

## 5.1. Прогрів бази (важливо при першому запуску)

Якщо база порожня або в ній мало оголошень, перший же запуск вважатиме новим
майже все і завалить чат (до `MAX_ITEMS_PER_RUN` × кількість пошуків
повідомлень). Прогрів робиться одноразовим контейнером із **навмисно недійсним
токеном**: оголошення зберігаються в базу, а відправки падають з 401 і в чат
нічого не йде.

```bash
cd ~/server/olx.kozelets.pp.ua/docker
docker run -d --name olxmon_seed --env-file ../.env.production \
  -e TELEGRAM_BOT_TOKEN=0:seed-run-no-telegram \
  -e DATABASE_PATH=/data/info-parser.db -e TZ=Europe/Kyiv \
  -e DAILY_NOTIFY_TIMES=none -e POLL_INTERVAL_SEC=86400 -e MAX_ITEMS_PER_RUN=1 \
  -e HTTP_ADDR=:8099 \
  -v olxmon_olxmon_data:/data olxmon:prod

docker logs -f olxmon_seed            # чекати рядки "N items, M non-ad, K new"
docker rm -f olxmon_seed              # прибрати після проходу всіх пошуків
docker compose -f docker-compose.prod.yml start
```

`DAILY_NOTIFY_TIMES=none` не проходить валідацію `HH:MM`, тому список часів
порожній і сервіс робить один прогін одразу після старту, а далі спить
`POLL_INTERVAL_SEC`. Окремий `HTTP_ADDR` — щоб не конфліктувати з основним
контейнером.

## 6. Перенести наявну базу (опційно)

Щоб не отримати всі старі оголошення як «нові»:

```bash
# локально: зупинити сервіс, скопіювати файл
scp ~/go/src/info-parser/data/info-parser.db deploy@188.245.71.3:/tmp/
# на сервері:
docker cp /tmp/info-parser.db olxmon_app:/data/info-parser.db
docker restart olxmon_app
rm /tmp/info-parser.db
```

Схема мігрує сама (`AutoMigrate` додасть `search_id`).

## 7. Оновлення

```bash
# локально — rsync з кроку 2, далі на сервері:
cd ~/server/olx.kozelets.pp.ua/docker
docker compose -f docker-compose.prod.yml up -d --build
```

Том з базою переживає перезбірку.

## 8. Бекап

```bash
docker run --rm -v olxmon_olxmon_data:/data -v ~/backups:/backup alpine \
  tar czf /backup/olxmon-$(date +%F).tar.gz -C /data .
```

## Ресурси

Контейнер обмежено `mem_limit: 192m` / `cpus: 0.5`; реальне споживання —
десятки мегабайт RAM і майже нуль CPU (кілька HTTP-запитів на добу).
Логи обмежені 3×10 МБ.
