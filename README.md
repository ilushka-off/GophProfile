# GophProfile — сервис аватарок

Микросервис принимает картинки, хранит их и отдаёт обратно, в том числе в виде
миниатюр. Выпускной проект курса «Go-разработчик» Яндекс Практикума.

- REST API на Go (chi) с PostgreSQL для метаданных и S3 (SeaweedFS) для файлов.
- Миниатюры 100x100 и 300x300 создаёт worker асинхронно: события идут через
  RabbitMQ **или** Kafka — брокер выбирается конфигом.
- Веб-интерфейс: форма загрузки с превью, drag&drop и прогрессом, галерея.
- Всё окружение поднимается одной командой через Docker Compose.
- Деплой в Kubernetes одним `helm upgrade --install`: автомасштабирование,
  пробы, мониторинг, сетевые политики и Pod Security `restricted`
  (см. [Kubernetes и Helm](#kubernetes-и-helm)).

## Архитектура

Схемы сервиса и его развёртывания в Kubernetes — [docs/architecture.md](docs/architecture.md),
API — [docs/openapi.yaml](docs/openapi.yaml) (OpenAPI 3.1).

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
| `cmd/server` | REST API и веб-интерфейс. Пишет в БД и S3, публикует события. Миграции применяет при старте (`MIGRATE_ON_START`) или подкомандой `server migrate up` (Helm-хук в Kubernetes) |
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
| `MIGRATE_ON_START` | `true` | Применять миграции при старте server. В Kubernetes `false`: их применяет Helm-хук до выката подов |
| `SHUTDOWN_DELAY` | `5s` | Сколько после SIGTERM server отвечает 503 на `/health`, продолжая обслуживать запросы, прежде чем закрыть HTTP-сервер (Shutdown ждёт активные запросы до 20 с) |
| `METRICS_ADDR` | `:9091` | Адрес отдельного HTTP-сервера с `/metrics`. У server он должен отличаться от `HTTP_ADDR` и не быть доступен клиентам: в метриках есть `user_id`. `make run-worker` поднимает метрики worker'а на `:9092` (`WORKER_METRICS_ADDR`) |

Переменные `POSTGRES_*`, `S3_PORT`, `RABBITMQ_*`, `KAFKA_EXTERNAL_PORT`,
`APP_PORT` из `.env.example` настраивают контейнеры docker compose, а `OTLP_ENDPOINT` —
`OTEL_EXPORTER_OTLP_ENDPOINT` server'а и worker'а в compose (по умолчанию пусто).

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
| GET | `/health` | Готовность: статус компонентов (вне `/api/v1`), 503 если что-то недоступно или сервис останавливается |
| GET | `/livez` | Живость процесса без проверки зависимостей (вне `/api/v1`) |

Полное описание — [docs/openapi.yaml](docs/openapi.yaml).

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
make up-observability                     # BROKER=kafka — с Kafka
# то же вручную; без OTLP_ENDPOINT трейсы в Jaeger не попадут
OTLP_ENDPOINT=jaeger:4317 docker compose --profile observability --profile rabbitmq up --build
```

| Адрес | Что там |
| --- | --- |
| http://localhost:3000/ | Grafana: дашборды в папке GophProfile (без входа; админ — `GRAFANA_ADMIN_*`) |
| http://localhost:16686/ | Jaeger UI: трейсы и граф зависимостей (System Architecture) |
| http://localhost:9090/targets | Prometheus: все цели должны быть UP |
| http://localhost:9093/ | Alertmanager |
Метрики server и worker отдаются на `:9091` только внутри сети compose: порт
не публикуется, потому что `avatars_storage_bytes` раскрывает `user_id` всех
пользователей. Посмотреть их можно в Prometheus или командой
`docker compose exec server-rabbitmq wget -qO- http://127.0.0.1:9091/metrics`.

Без профиля сервис работает как раньше: `OTLP_ENDPOINT` пуст, трейсы создаются
(`trace_id` есть в логах и в `X-Trace-ID`), но никуда не отправляются.

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
| `avatars_storage_bytes{user_id}`, `avatars_storage_bytes_all` | Объём оригиналов неудалённых аватарок (считается в БД при опросе). См. оговорку ниже |
| `avatars_processing_total{type,status}`, `avatars_processing_duration_seconds` | События worker'а: успех или DLQ, время обработки с повторами |
| `avatars_processing_lag_seconds` | Сколько событие ждало в брокере до начала обработки |
| `pgxpool_*` | Пул соединений PostgreSQL: занятые, свободные, ожидание |
| `go_*`, `process_*` | Go runtime и процесс |

`avatars_storage_bytes{user_id}` — одна серия на каждого пользователя, число
серий растёт без ограничения. Для учебного проекта это допустимо, но в реальной
системе так делать нельзя: Prometheus не рассчитан на миллионы серий одной
метрики, а каждый опрос делает `GROUP BY user_id` по всей таблице. Там стоит
оставить только `avatars_storage_bytes_all` или отдавать топ-N пользователей,
а объём по пользователю брать из БД.

Дашборды Grafana лежат в `deploy/helm/gophprofile/dashboards` и
подключаются при старте (правки в UI не сохраняются):

- **Service overview** — доступность server и worker, RPS, доля 5xx, p95,
  последние ошибки из Loki;
- **RED** — rate, errors, duration (p50/p95/p99) по маршрутам, переменная `$service`.
- **Resource utilization** — CPU и память контейнеров, горутины, heap, паузы GC,
  пул pgx, очереди RabbitMQ и DLQ или consumer lag Kafka.
- **Business KPIs** — загрузки в минуту, доля отказов, удаления, объём
  хранилища и топ-10 пользователей, время создания миниатюр и processing lag.

### Алерты

Правила Prometheus — в `deploy/helm/gophprofile/files/prometheus/alerts.yml`,
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

## Kubernetes и Helm

Сервис упакован в два чарта:

| Чарт | Что ставит |
| --- | --- |
| `deploy/helm/gophprofile` | server, worker, Job миграций (хук `pre-install,pre-upgrade`), Service, Ingress, HPA, PDB, ConfigMap/Secret, ServiceAccount, NetworkPolicy, ServiceMonitor, PrometheusRule и дашборды |
| `deploy/helm/gophprofile-infra` | PostgreSQL, S3 (SeaweedFS), RabbitMQ или Kafka — только для локального кластера; в проде — управляемые сервисы |

Подробности ресурсов, порядок хуков и graceful shutdown — в
[docs/architecture.md](docs/architecture.md).

### Локальный запуск в Rancher Desktop

Нужны Rancher Desktop (Kubernetes 1.30+), `helm`, `kubectl`.

1. В Rancher Desktop включить Kubernetes и **отключить Traefik**
   (Preferences → Kubernetes): чарт рассчитан на ingress-nginx.
2. Поднять всё одной командой:

   ```bash
   make k8s-up                # RabbitMQ
   make k8s-up BROKER=kafka   # или Kafka
   ```

   По шагам это `make k8s-ingress` (ingress-nginx), `make k8s-observability`
   (kube-prometheus-stack, Loki, Alloy, Jaeger в namespace `monitoring`),
   `make k8s-images` (образы `gophprofile/server` и `gophprofile/worker` с тегом
   `appVersion` чарта), `make k8s-infra` (namespace `gophprofile` с Pod Security
   `restricted` и зависимости) и `make k8s-deploy` (приложение).
   С движком dockerd вместо containerd: `make k8s-up IMAGE_BUILDER=docker`.
3. Добавить `127.0.0.1 avatars.local` в hosts (`/etc/hosts` или
   `C:\Windows\System32\drivers\etc\hosts`) и открыть
   http://avatars.local/web/upload.
4. Проверить: `make k8s-test` (helm test ходит в `/health` через Service),
   `kubectl -n gophprofile get pods,hpa`.

| Что | Как открыть |
| --- | --- |
| Grafana (admin/admin) | `kubectl -n monitoring port-forward svc/kps-grafana 3000:80` → http://localhost:3000 |
| Prometheus | `kubectl -n monitoring port-forward svc/kps-kube-prometheus-stack-prometheus 9090` |
| Jaeger | `kubectl -n monitoring port-forward svc/jaeger-query 16686` |
| RabbitMQ UI | `kubectl -n gophprofile port-forward svc/rabbitmq 15672` |

Удалить приложение и зависимости — `make k8s-down` (стек мониторинга и
ingress-nginx остаются).

### Деплой в другой кластер

```bash
kubectl apply -f deploy/k8s/namespace.yaml
# Secret с ключами DATABASE_URI, S3_ACCESS_KEY, S3_SECRET_KEY (и RABBITMQ_URL)
# создаётся заранее — в git секретов нет.
helm upgrade --install gophprofile deploy/helm/gophprofile -n gophprofile \
  -f deploy/helm/gophprofile/values-prod.yaml --set server.image.tag=<версия> --set worker.image.tag=<версия>
```

`values-prod.yaml` — пример: внешние PostgreSQL, S3 и Kafka, TLS через
cert-manager, HPA server 2…10 и worker 1…6, сэмплинг трейсов 10%,
NetworkPolicy к зависимостям по CIDR. Все параметры и их проверка —
`values.yaml` и `values.schema.json` (например, тег `latest` запрещён: с ним
`helm upgrade` не перезапускает поды).

### Как это устроено

- **Пробы.** startup и liveness — `/livez` (процесс жив), readiness — `/health`
  (БД, S3, брокер). Если бы liveness проверяла зависимости, падение БД
  перезапускало бы все поды по кругу. У worker'а те же эндпоинты на порту 9091.
- **Миграции** применяет Job-хук `server migrate up` до выката подов
  (`MIGRATE_ON_START=false`). Упавшая миграция останавливает `helm upgrade`,
  старая версия продолжает работать.
- **Масштабирование.** HPA `autoscaling/v2` по CPU 70% и памяти 80% от
  requests; при включённом HPA поле `replicas` не рендерится. С Kafka
  `maxReplicas` worker'а не больше числа партиций `avatar-events`.
  Проверка: нагрузка на `POST /api/v1/avatars` (`hey`, `k6`) и `kubectl get hpa -w`.
- **Graceful shutdown.** server по SIGTERM отдаёт 503 на `/health`, 5 с
  продолжает обслуживать запросы, затем ждёт активные до 20 с; worker
  дорабатывает текущее сообщение до 45 с. `kubectl rollout restart` под
  нагрузкой проходит без 5xx.
- **Ресурсы.** У каждого контейнера requests и limits, `GOMEMLIMIT` ≈ 90%
  от limit памяти; изменение ConfigMap/Secret перезапускает поды
  (аннотации `checksum/*`).
- **Безопасность.** Namespace в режиме Pod Security `restricted`; образы
  distroless nonroot (UID 65532), read-only root FS, без capabilities,
  seccomp `RuntimeDefault`. NetworkPolicy: default deny, разрешены только
  ingress-nginx → server, monitoring → метрики и приложение → DNS, PostgreSQL,
  S3, брокер, Jaeger. Если CNI ограничивает трафик с узлов (Calico, Cilium в
  строгих конфигурациях), для проб kubelet задайте CIDR узлов в
  `networkPolicy.probeSources`. ServiceAccount без токена API и без прав.
- **Мониторинг.** ServiceMonitor по порту `metrics` (job `avatar-service` и
  `avatar-worker`, как в compose), алерты этапа 2 — PrometheusRule, дашборды —
  ConfigMap для sidecar Grafana. Правила и дашборды — одни файлы для compose и
  кластера (`deploy/helm/gophprofile/files`, `dashboards`). Без CRD Prometheus
  Operator чарт ставится, просто без них.

Проверка чартов (её же запускает CI): `make helm-check` — `helm lint` и
kubeconform отрендеренных манифестов для local и prod, обоих брокеров,
с CRD мониторинга и без.

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
make helm-check   # helm lint и kubeconform обоих чартов
```

Интеграционные тесты поднимают PostgreSQL, SeaweedFS, RabbitMQ и Kafka через
testcontainers и пропускаются с `-short` или без Docker. Тесты S3 можно
направить на уже запущенное хранилище: `TEST_S3_ENDPOINT`, `TEST_S3_ACCESS_KEY`,
`TEST_S3_SECRET_KEY`.

Миграции лежат в `internal/repository/postgres/migrations`, встроены в бинарник
и применяются server'ом при старте (golang-migrate, `MIGRATE_ON_START=true`)
или подкомандой `server migrate up`. Вручную: `make migrate`.

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
docker/Dockerfile              multi-stage сборка, цели server и worker (distroless nonroot)
deploy/observability           конфиги Prometheus, Loki, Alloy, Grafana для docker compose
deploy/helm/gophprofile        Helm-чарт приложения; dashboards/ и files/prometheus общие с compose
deploy/helm/gophprofile-infra  PostgreSQL, S3, брокер для локального кластера
deploy/k8s                     namespace и values стека наблюдаемости для кластера
docs                           OpenAPI и схема архитектуры
```
