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

**Если брокер недоступен.** server публикует событие после записи в БД и S3.
Если публикация не удалась, загрузка откатывается (строка и оригинал
удаляются), а удаление — снимается пометка `deleted_at`; клиент получает 500,
и запрос можно просто повторить. `/health` при каждом запросе пробует
переподключиться к брокеру, поэтому после его рестарта сам возвращается к 200.
worker при потере брокера завершается с ошибкой (например,
`consume avatars.delete: rabbitmq channel is closed`) — это ожидаемо: в compose
его перезапускает `restart: unless-stopped`, а при локальном запуске его нужно
перезапустить вручную.

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
| `MAX_UPLOAD_SIZE` | `10485760` | Лимит файла в байтах, от 1 до 10 MiB (проверяется при старте сервиса) |
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
| `CORS_ALLOWED_ORIGINS` | пусто | Origins через запятую (`https://app.example`) или `*`, которым браузер разрешает запросы к API. Пусто — CORS выключен |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `jaeger:4317` | Куда отправлять трейсы по OTLP gRPC (`host:port` или `http://host:port`). Пусто — трейсы не отправляются |
| `OTEL_TRACES_SAMPLER_ARG` | `1` | Доля записываемых трейсов от 0 до 1; продолжение входящего трейса решает родитель |
| `DEPLOYMENT_ENVIRONMENT` | `local` | `deployment.environment.name` в трейсах |
| `METRICS_ADDR` | `:9091` | Адрес `/metrics` у worker'а (у server метрики на `HTTP_ADDR`) |

Переменные `POSTGRES_*`, `S3_PORT`, `RABBITMQ_*`, `KAFKA_EXTERNAL_PORT`,
`APP_PORT` из `.env.example` настраивают контейнеры docker compose.

## API

Базовый путь `/api/v1`. Пользователь определяется заголовком `X-User-ID`
(авторизации в v1 нет): 1–255 символов, латиница, цифры и `. _ - @`.
Заголовок обязателен для загрузки и удаления.

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

## Наблюдаемость

Профиль `observability` поднимает рядом с сервисом Prometheus, Alertmanager,
Jaeger, Loki, Alloy, Grafana и cAdvisor (порты — в `.env.example`):

```bash
docker compose --profile observability --profile rabbitmq up --build   # или с kafka
make up-observability                                                  # то же через make
```

| Адрес | Что там |
| --- | --- |
| http://localhost:3000/ | Grafana: дашборды в папке GophProfile (без входа; админ — `GRAFANA_ADMIN_*`) |
| http://localhost:16686/ | Jaeger UI: трейсы и граф зависимостей (System Architecture) |
| http://localhost:9090/targets | Prometheus: все цели должны быть UP |
| http://localhost:9093/ | Alertmanager |
| http://localhost:8080/metrics | Метрики server (worker — на `:9091` внутри сети compose) |

Без профиля сервис работает как раньше: трейсы, которые некуда отправить,
теряются, ошибки экспорта пишутся в лог.

### Трейсы

server и worker пишут трейсы OpenTelemetry и отправляют их в Jaeger по OTLP
gRPC (`OTEL_EXPORTER_OTLP_ENDPOINT`). Загрузка аватарки видна одним трейсом от
HTTP-запроса до готовых миниатюр:

```
POST /api/v1/avatars                         avatar-service
└─ upload_avatar                             user.id, avatar.id, file.size, mime.type
   ├─ s3.PutObject                           s3.bucket, s3.key, file.size
   ├─ INSERT                                 db.system.name=postgresql (текст запроса без параметров)
   └─ publish avatars.exchange               kind=producer, traceparent → заголовки сообщения
      └─ process avatars.process             avatar-worker, kind=consumer
         ├─ SELECT (дедупликация, статус)
         └─ process_thumbnails               thumbnail.sizes
            ├─ s3.GetObject
            ├─ s3.PutObject × 2
            └─ UPDATE
```

Контекст трейса едет в заголовках сообщения (AMQP headers в RabbitMQ, record
headers в Kafka), поэтому трейс сквозной для обоих брокеров. Имя HTTP-спана —
шаблон маршрута (`GET /api/v1/avatars/{avatar_id}`), ответы 5xx помечают спан
ошибкой, `/health` и `/metrics` не трейсятся. Каждый ответ API несёт заголовок
`X-Trace-ID`: по нему трейс находится в Jaeger, а логи запроса — в Loki.

Как проверить: загрузить файл, взять `X-Trace-ID` из ответа и открыть
`http://localhost:16686/trace/<trace_id>`.

```bash
curl -si -H 'X-User-ID: user-42' -F file=@me.png http://localhost:8080/api/v1/avatars | grep -i x-trace-id
```

### Метрики и дашборды

Prometheus опрашивает раз в 15 секунд server, worker, брокер (плагин
`rabbitmq_prometheus` или kafka-exporter), cAdvisor и SeaweedFS и хранит
данные 7 дней. Метрики сервиса:

| Метрика | Что считает |
| --- | --- |
| `http_requests_total`, `http_request_duration_seconds`, `http_requests_in_flight` | RED по `method`, `route` (шаблон маршрута), `status` |
| `avatars_uploads_total{status}` | Загрузки: `success`, `rejected` (файл не прошёл проверки), `error` (сбой) |
| `avatars_upload_duration_seconds`, `avatars_deletes_total` | Длительность успешной загрузки, удаления |
| `avatars_storage_bytes{user_id}`, `avatars_storage_bytes_total` | Объём оригиналов неудалённых аватарок (считается в БД при опросе) |
| `avatars_processing_total{type,status}`, `avatars_processing_duration_seconds` | События worker'а: успех или DLQ, время обработки с повторами |
| `avatars_processing_lag_seconds` | Сколько событие ждало в брокере до начала обработки |
| `pgxpool_*` | Пул соединений PostgreSQL: занятые, свободные, ожидание |
| `go_*`, `process_*` | Go runtime и процесс |

Дашборды Grafana лежат в `deploy/observability/grafana/dashboards` и
подключаются при старте (правки в UI не сохраняются):

- **Service overview** — доступность server и worker, RPS, доля 5xx, p95,
  последние ошибки из Loki;
- **RED** — rate, errors, duration (p50/p95/p99) по маршрутам, переменная `$service`.
- **Resource utilization** — CPU и память контейнеров, горутины, heap, паузы GC,
  пул pgx, очереди RabbitMQ и DLQ или consumer lag Kafka.
- **Business KPIs** — загрузки в минуту, доля отказов, удаления, объём
  хранилища и топ-10 пользователей, время создания миниатюр и processing lag.

### Алерты

Правила Prometheus — в `deploy/observability/prometheus/rules/alerts.yml`,
сработавшие алерты уходят в Alertmanager (http://localhost:9093) и видны
в Grafana → Alerting.

| Алерт | Условие |
| --- | --- |
| `HighErrorRate` | Больше 5% ответов 5xx за 5 минут (по сумме `rate()` всех маршрутов) |
| `HighResponseTime` | p95 длительности ответа больше 1 с за 5 минут |
| `DLQNotEmpty` | В `avatars.dlq` или `avatar-events.dlq` есть сообщения |
| `ServiceDown` | server или worker не отвечает на `/metrics` или не запущен |
| `ErrorLogsSpike` | Больше 10 строк ERROR за 5 минут (Loki ruler, см. «Логи») |

Пример получателя в `deploy/observability/alertmanager/alertmanager.yml` — webhook
на `http://host.docker.internal:5001/alerts`. Тесты правил — `make alerts-test`
(promtool в Docker), они же идут в CI.

### Логи

server и worker пишут JSON в stdout: `time`, `level`, `msg`, `service`
(`avatar-service` или `avatar-worker`), `version` и, если запись сделана внутри
трейса, `trace_id` и `span_id`. Уровень задаёт `LOG_LEVEL`. Каждый HTTP-запрос
пишется access-логом с `method`, `route`, `status`, `duration_ms`. Содержимое
файлов, пароли и строки подключения в лог не попадают.

Alloy собирает логи всех контейнеров проекта и отправляет в Loki с лейблами
`service`, `level` и `container`; `trace_id` остаётся в теле строки. Примеры
запросов в Grafana → Explore → Loki:

```logql
{service="avatar-service"} | json | level="ERROR"
{service=~"avatar-.+"} | json | trace_id="4bf92f3577b34da6a3ce929d0e0e4736"
```

В строке лога `trace_id` — ссылка на трейс в Jaeger, а из спана в Jaeger есть
переход к логам этого трейса (*Logs for this span*).

Алерт `ErrorLogsSpike` (Loki ruler, `deploy/observability/loki/rules/`)
срабатывает, если у сервиса больше 10 строк уровня ERROR за 5 минут. Проверить:
остановить базу и сделать несколько запросов — каждый ответ 500 пишет ошибку.

```bash
docker compose stop postgres
for i in $(seq 15); do curl -s -o /dev/null http://localhost:8080/api/v1/users/u/avatars; done
docker compose start postgres
```

Через 1–2 минуты алерт виден в Grafana → Alerting → Alert rules (источник Loki).

## Разработка

server и worker без переменных окружения не стартуют (например, `database URI
not found`): `DATABASE_URI`, `S3_*` и адрес брокера обязательны. `make run`
берёт их из `.env`; при запуске через `go run` или из GoLand подключите `.env`
в Run Configuration (поле *Environment* или плагин EnvFile) или экспортируйте
переменные вручную.

```bash
make run          # server локально (переменные из .env)
make run-worker   # worker локально
make test         # все тесты, включая интеграционные (нужен Docker)
make test-short   # только unit-тесты, без контейнеров
make cover        # покрытие; падает, если ниже 50%
make lint         # golangci-lint v2 (конфиг .golangci.yml)
make alerts-test  # promtool: конфиг Prometheus и тесты алертов (нужен Docker)
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
internal/middleware, httpx     логирование, трейсинг, метрики HTTP, recover, формат ответов
internal/observability         OpenTelemetry, JSON-логи с trace_id, реестр метрик
internal/services              бизнес-логика аватарок
internal/repository/postgres   PostgreSQL (pgx) и миграции
internal/storage               трейсинг операций с файлами
internal/storage/s3            S3-клиент (minio-go)
internal/broker                интерфейсы брокера, события, retry
internal/broker/{rabbitmq,kafka,factory}  реализации и выбор по BROKER
internal/imaging               кодирование и ресайз картинок
internal/worker                обработка событий worker'ом
web                            HTML-шаблоны (встроены в бинарник)
docker/Dockerfile              multi-stage сборка, цели server и worker
deploy/observability           конфиги Prometheus, Loki, Alloy, Grafana
```
