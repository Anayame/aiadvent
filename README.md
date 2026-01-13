# Go каркас: Auth + LLM (OpenRouter) + Telegram Webhook

## Что это
Минимальный, но production-friendly каркас Go-приложения (один бинарник) c тремя сервисами:
- Auth Service: простой парольный логин, in-memory сессии с TTL, интерфейс для замены на внешнее хранилище.
- LLM Service: клиент OpenRouter с ретраями, таймаутом и конфигом модели по умолчанию.
- Telegram Webhook Service: обработка команд бота и проксирование запросов к LLM.

Используются Go >= 1.22, `chi` для роутинга и стандартный `slog` для логов.

## Быстрый старт
```bash
cp .env.example .env   # заполните переменные
make run               # go run ./cmd/app
```

Другие команды:
- `make test` — запустить тесты
- `make lint` — базовая проверка (go vet)
- `make build` — собрать бинарник в `bin/app`

## Переменные окружения
- `HTTP_ADDR` — адрес HTTP-сервера, по умолчанию `:8080`
- `LOG_LEVEL` — `debug|info|warn|error`, по умолчанию `info`
- `ADMIN_PASSWORD` — пароль для `/login`
- `SESSION_TTL` — длительность жизни сессии, например `2h`; значение `0` делает сессии бессрочными
- `AUTH_STORE_TYPE` — `file|memory`, по умолчанию `file`
- `AUTH_STORE_PATH` — путь к файлу сессий для `file` store, по умолчанию `/data/auth_sessions.json`
- `OPENROUTER_API_KEY` — ключ OpenRouter
- `OPENROUTER_BASE_URL` — базовый URL, по умолчанию `https://openrouter.ai/api/v1`
- `OPENROUTER_DEFAULT_MODEL` — модель по умолчанию, обязательна для LLM
- `TELEGRAM_BOT_TOKEN` — токен бота
- `TELEGRAM_API_BASE_URL` — базовый URL Telegram API, по умолчанию `https://api.telegram.org`
- `TELEGRAM_WEBHOOK_SECRET` — секрет заголовка `X-Telegram-Bot-Api-Secret-Token` (если пустой — проверка отключена)

## HTTP эндпоинты
- `GET /ping` — health-check, 200 OK
- `POST /telegram/webhook` — прием Telegram update, опционально проверяется `X-Telegram-Bot-Api-Secret-Token`

Формат ошибок (JSON):
```json
{ "error": { "code": "forbidden", "message": "invalid webhook secret" } }
```

## Команды бота
- `/start` — приветствие и подсказка
- `/login <password>` — вход; пароль сверяется с `ADMIN_PASSWORD`
- `/logout` — выход, удаление сессии
- `/me` — показать telegram user id и статус авторизации
- `/ask <текст>` — запрос к LLM (требует авторизации)
- Просто текст без команды:
  - если авторизован — трактуется как `/ask <text>`
  - иначе — подсказка залогиниться

## Примеры запросов
Health-check:
```bash
curl -i http://localhost:8080/ping
```

Имитация Telegram webhook (секрет можно опустить, если не задан):
```bash
curl -i -X POST http://localhost:8080/telegram/webhook \
  -H "Content-Type: application/json" \
  -H "X-Telegram-Bot-Api-Secret-Token: your-secret" \
  -d '{"message":{"message_id":1,"text":"/start","chat":{"id":123},"from":{"id":123,"username":"tester"}}}'
```

## Структура проекта
- `cmd/app` — точка входа
- `internal/config` — загрузка конфигурации из env
- `internal/httpserver` — chi-роутер, middleware, health
- `internal/middleware` — request-id, логирование, recover
- `internal/auth` — сервис аутентификации и in-memory хранилище сессий
- `internal/llm` — интерфейс LLM и клиент OpenRouter
- `internal/transport` — общие HTTP клиент-утилиты
- `internal/telegram` — webhook хендлер и клиент Telegram Bot API

## Завершение работы
Приложение поддерживает graceful shutdown по `SIGINT/SIGTERM`.
