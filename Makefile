ENV_FILE ?= .env

-include $(ENV_FILE)
export

BIN_DIR        := bin
MIGRATIONS_DIR := internal/repository/postgres/migrations
COVER_FILE     := coverage.out
COVER_MIN      := 50
BROKER         ?= rabbitmq
# /metrics worker'а при локальном запуске: METRICS_ADDR из .env (:9091) занят server'ом.
WORKER_METRICS_ADDR ?= :9092
# golangci-lint v2 собран под Go 1.26+; версия закреплена, чтобы у всех были одни правила.
GOLANGCI_LINT  ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

COMPOSE := docker compose --env-file $(ENV_FILE) --profile $(BROKER)

OBSERVABILITY_DIR := deploy/observability
# promtool из того же образа, что и Prometheus в docker compose.
PROMTOOL := docker run --rm -v "$(CURDIR)/$(OBSERVABILITY_DIR)/prometheus:/prometheus:ro" -w /prometheus 	--entrypoint promtool prom/prometheus:v3.15.0

.PHONY: help build run run-worker test test-short cover lint alerts-test migrate migrate-down up down up-observability logs

help: ## Показать список команд
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(firstword $(MAKEFILE_LIST)) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-14s %s\n", $$1, $$2}'

build: ## Собрать server и worker в bin/
	go build -o $(BIN_DIR)/server ./cmd/server
	go build -o $(BIN_DIR)/worker ./cmd/worker

run: ## Запустить server локально
	go run ./cmd/server

run-worker: METRICS_ADDR = $(WORKER_METRICS_ADDR)
run-worker: ## Запустить worker локально (/metrics на WORKER_METRICS_ADDR)
	go run ./cmd/worker

test: ## Все тесты, включая интеграционные (нужен Docker для testcontainers)
	go test -count=1 ./...

test-short: ## Только unit-тесты, без контейнеров
	go test -count=1 -short ./...

cover: ## Тесты с отчётом о покрытии; падает, если покрытие ниже COVER_MIN
	go test -count=1 -coverprofile=$(COVER_FILE) ./...
	go tool cover -func=$(COVER_FILE)
	@go tool cover -func=$(COVER_FILE) | awk -v min=$(COVER_MIN) '/^total:/ { sub("%", "", $$3); \
		if ($$3 + 0 < min) { printf "coverage %s%% is below %s%%\n", $$3, min; exit 1 } \
		else printf "coverage %s%% >= %s%%\n", $$3, min }'

lint: ## Запустить golangci-lint (конфиг .golangci.yml)
	$(GOLANGCI_LINT) run ./...

alerts-test: ## Проверить конфиг Prometheus и прогнать тесты алертов (нужен Docker)
	$(PROMTOOL) check config --syntax-only prometheus.yml
	$(PROMTOOL) check rules rules/alerts.yml
	$(PROMTOOL) test rules tests/alerts_test.yml

migrate: ## Применить миграции к DATABASE_URI
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URI)" up

migrate-down: ## Откатить последнюю миграцию
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URI)" down 1

up: ## Поднять окружение в docker compose (BROKER=rabbitmq|kafka)
	$(COMPOSE) up -d --build

up-observability: OTLP_ENDPOINT = jaeger:4317
up-observability: ## Поднять окружение вместе с профилем observability
	$(COMPOSE) --profile observability up -d --build

down: ## Остановить окружение (включая observability)
	$(COMPOSE) --profile observability down

logs: ## Логи server и worker
	$(COMPOSE) logs -f server-$(BROKER) worker-$(BROKER)
