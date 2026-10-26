ENV_FILE ?= .env

-include $(ENV_FILE)
export

BIN_DIR        := bin
MIGRATIONS_DIR := internal/repository/postgres/migrations
COVER_FILE     := coverage.out
COVER_MIN      := 50
BROKER         ?= rabbitmq
# golangci-lint v2 собран под Go 1.26+; версия закреплена, чтобы у всех были одни правила.
GOLANGCI_LINT  ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

COMPOSE := docker compose --env-file $(ENV_FILE) -f docker/docker-compose.yaml --profile $(BROKER)

.PHONY: help build run run-worker test test-short cover lint migrate migrate-down up down logs

help: ## Показать список команд
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(firstword $(MAKEFILE_LIST)) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-14s %s\n", $$1, $$2}'

build: ## Собрать server и worker в bin/
	go build -o $(BIN_DIR)/server ./cmd/server
	go build -o $(BIN_DIR)/worker ./cmd/worker

run: ## Запустить server локально
	go run ./cmd/server

run-worker: ## Запустить worker локально
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

migrate: ## Применить миграции к DATABASE_URI
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URI)" up

migrate-down: ## Откатить последнюю миграцию
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URI)" down 1

up: ## Поднять окружение в docker compose (BROKER=rabbitmq|kafka)
	$(COMPOSE) up -d --build

down: ## Остановить окружение
	$(COMPOSE) down

logs: ## Логи app
	$(COMPOSE) logs -f app
