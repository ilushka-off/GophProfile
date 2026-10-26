# GophProfile — сервис аватарок

Микросервис принимает картинки, хранит их и отдаёт обратно, в том числе в виде
миниатюр. Выпускной проект курса «Go-разработчик» Яндекс Практикума.

- REST API на Go (chi) с PostgreSQL для метаданных и S3 (SeaweedFS) для файлов.
- Миниатюры 100x100 и 300x300 создаёт worker асинхронно: события идут через
  RabbitMQ **или** Kafka — брокер выбирается конфигом.
- Веб-интерфейс: форма загрузки с превью, drag&drop и прогрессом, галерея.
- Всё окружение поднимается одной командой через Docker Compose.

## Архитектура

```
 браузер / клиент ──HTTP──► server ──► PostgreSQL (метаданные)
                              │   └──► S3: originals/{id}.{ext}
                              │
                              └─ avatar.uploaded / avatar.deleted ─► RabbitMQ | Kafka ─► worker
                                                                                  ├──► S3: thumbnails/{id}/{size}.jpg
                                                                                  └──► PostgreSQL (статус обработки)
```

| Компонент | Что делает |
| --- | --- |
| `cmd/server` | REST API и веб-интерфейс. Применяет миграции при старте, пишет в БД и S3, публикует события |
| `cmd/worker` | Читает события: создаёт миниатюры, удаляет файлы удалённых аватарок |
| PostgreSQL | Таблицы `avatars` и `processed_messages` (идемпотентность worker'а) |
| SeaweedFS | S3-совместимое хранилище. Образы MinIO больше не публикуются на Docker Hub, поэтому в compose — SeaweedFS; клиент (`minio-go`) работает с любым S3 |
| RabbitMQ | Exchange `avatars.exchange` (topic), очереди `avatars.process`, `avatars.delete`, `avatars.dlq` |
| Kafka | Топик `avatar-events` (ключ — `avatar_id`), consumer group `avatars-worker`, топик `avatar-events.dlq` |

**Гарантии обработки.** Оба брокера доставляют сообщение «хотя бы один раз»,
поэтому worker идемпотентен: пропускает аватарки с готовыми миниатюрами и уже
обработанные `message_id`, удаление отсутствующего файла — не ошибка. Временные
ошибки (БД, S3) повторяются до 3 раз с задержкой 1, 2 и 4 секунды; после этого
или сразу при постоянной ошибке (битая картинка) аватарка получает статус
`failed`, а сообщение уходит в DLQ. Поведение одинаковое для RabbitMQ и Kafka.

## Быстрый старт

Нужен Docker с Compose v2.

```bash
cp .env.example .env
docker compose --profile rabbitmq up --build   # или:
docker compose --profile kafka up --build
```

Профиль выбирает брокер и запускает server и worker, настроенные на него;
одновременно включать оба профиля не нужно. Тот же запуск через make:
`make up` (брокер берётся из `BROKER` в `.env`, например `make up BROKER=kafka`),
`make logs`, `make down`.

После старта:

| Адрес | Что там |
| --- | --- |
| http://localhost:8080/ | Веб-интерфейс (редирект на `/web/upload`) |
| http://localhost:8080/web/gallery/{user_id} | Галерея пользователя |
| http://localhost:8080/health | Статус БД, S3 и брокера |
| http://localhost:15672/ | RabbitMQ management (профиль `rabbitmq`) |

## Переменные окружения

server и worker читают одни и те же переменные.

| Переменная | По умолчанию | Описание |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | Адрес HTTP-сервера |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `DATABASE_URI` | — (обязательная) | `postgres://user:pass@host:5432/db?sslmode=disable` |
| `MAX_UPLOAD_SIZE` | `10485760` | Лимит файла в байтах, не больше 10 MB |
| `S3_ENDPOINT` | — (обязательная) | `host:port` S3-совместимого хранилища |
| `S3_REGION` | `us-east-1` | Регион бакета |
| `S3_ACCESS_KEY`, `S3_SECRET_KEY` | — (обязательные) | Ключи доступа |
| `S3_BUCKET` | — (обязательная) | Бакет; server создаёт его при старте, если нет |
| `S3_USE_SSL` | `false` | HTTPS к S3 |
| `BROKER` | `rabbitmq` | `rabbitmq` или `kafka` |
| `RABBITMQ_URL` | — (для `rabbitmq`) | `amqp://user:pass@host:5672/` |
| `KAFKA_BROKERS` | — (для `kafka`) | Список брокеров через запятую |
| `RATE_LIMIT_RPS` | `20` | Запросов в секунду с одного IP к `/api/v1` и `/web`; `0` — без ограничения. Сверх лимита — 429 с `Retry-After` |
| `RATE_LIMIT_BURST` | `40` | Сколько запросов подряд можно сделать сверх средней скорости |

Переменные `POSTGRES_*`, `S3_PORT`, `RABBITMQ_*`, `KAFKA_EXTERNAL_PORT`,
`APP_PORT` из `.env.example` настраивают контейнеры docker compose.

## API

Базовый путь `/api/v1`. Пользователь определяется заголовком `X-User-ID`
(авторизации в v1 нет); он обязателен для загрузки и удаления.

| Метод | Путь | Что делает |
| --- | --- | --- |
| POST | `/avatars` | Загрузить аватарку: multipart, поле `file`, JPEG/PNG/WebP до 10 MB |
| GET | `/avatars/{avatar_id}?size=&format=` | Картинка: `size` = `100x100`, `300x300`, `original`; `format` = `jpeg`, `png`, `webp` |
| GET | `/avatars/{avatar_id}/metadata` | Метаданные и ссылки на готовые миниатюры |
| GET | `/users/{user_id}/avatar` | Текущая (последняя) аватарка пользователя, параметры как выше |
| GET | `/users/{user_id}/avatars` | Список аватарок пользователя, новые сверху |
| DELETE | `/avatars/{avatar_id}` | Удалить свою аватарку |
| DELETE | `/users/{user_id}/avatar` | Удалить свою текущую аватарку |
| GET | `/health` | Статус компонентов (вне `/api/v1`), 503 если что-то недоступно |

Коды ответов: 200/201 при успехе, 204 на DELETE, 304 на совпавший `If-None-Match`,
400 — ошибка валидации, 403 — чужая аватарка, 404 — нет или удалена, 413 — файл
больше лимита, 500 — внутренняя ошибка. Ошибки — JSON
`{"error": "...", "details": "..."}`.

### Примеры

```bash
# Загрузить
curl -s -H 'X-User-ID: user-42' -F file=@me.jpg http://localhost:8080/api/v1/avatars
# {"id":"0b6c6c9e-…","user_id":"user-42","url":"/api/v1/avatars/0b6c6c9e-…","status":"processing","created_at":"2026-10-06T10:00:00Z"}

# Метаданные: status станет ready, когда worker создаст миниатюры
curl -s http://localhost:8080/api/v1/avatars/0b6c6c9e-…/metadata

# Миниатюра 300x300 в WebP
curl -s -o thumb.webp 'http://localhost:8080/api/v1/avatars/0b6c6c9e-…?size=300x300&format=webp'

# Текущая аватарка и список
curl -s -o current.jpg http://localhost:8080/api/v1/users/user-42/avatar
curl -s http://localhost:8080/api/v1/users/user-42/avatars

# Удалить
curl -s -X DELETE -H 'X-User-ID: user-42' http://localhost:8080/api/v1/avatars/0b6c6c9e-…

# Здоровье
curl -s http://localhost:8080/health
# {"status":"ok","broker_type":"rabbitmq","components":{"broker":"ok","database":"ok","storage":"ok"}}
```

### Веб-интерфейс

| Метод | Путь | Что делает |
| --- | --- | --- |
| GET | `/web/upload` | Форма загрузки. С JavaScript отправляет файл в API с прогрессом и drag&drop |
| POST | `/web/upload` | Та же загрузка из обычной HTML-формы (без JS), затем редирект на галерею |
| GET | `/web/gallery/{user_id}` | Миниатюры 300x300 с размером, форматом, статусом и кнопкой удаления |

## Разработка

```bash
make run          # server локально (переменные из .env)
make run-worker   # worker локально
make test         # все тесты, включая интеграционные (нужен Docker)
make test-short   # только unit-тесты, без контейнеров
make cover        # покрытие; падает, если ниже 50%
make lint         # golangci-lint v2 (конфиг .golangci.yml)
```

Интеграционные тесты поднимают PostgreSQL, SeaweedFS, RabbitMQ и Kafka через
testcontainers и пропускаются с `-short` или без Docker. Тесты S3 можно
направить на уже запущенное хранилище: `TEST_S3_ENDPOINT`, `TEST_S3_ACCESS_KEY`,
`TEST_S3_SECRET_KEY`.

Миграции лежат в `internal/repository/postgres/migrations`, встроены в бинарник
и применяются server'ом при старте (golang-migrate). Вручную: `make migrate`.

## Структура

```
cmd/server, cmd/worker         точки входа
internal/config                конфиг из переменных окружения
internal/domain                модель Avatar, ошибки, ключи S3
internal/handlers              HTTP-обработчики API и веб-интерфейса, роутер
internal/middleware, httpx     логирование, recover, формат ответов
internal/services              бизнес-логика аватарок
internal/repository/postgres   PostgreSQL (pgx) и миграции
internal/storage/s3            S3-клиент (minio-go)
internal/broker                интерфейсы брокера, события, retry
internal/broker/{rabbitmq,kafka,factory}  реализации и выбор по BROKER
internal/imaging               кодирование и ресайз картинок
internal/worker                обработка событий worker'ом
web                            HTML-шаблоны (встроены в бинарник)
docker/Dockerfile              multi-stage сборка, цели server и worker
```
