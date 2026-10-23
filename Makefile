ENV_FILE ?= .env

-include $(ENV_FILE)
export

BIN_DIR        := bin
MIGRATIONS_DIR := internal/repository/postgres/migrations
COVER_FILE     := coverage.out
BROKER         ?= rabbitmq

COMPOSE := docker compose --env-file $(ENV_FILE) -f docker/docker-compose.yaml --profile $(BROKER)

.PHONY: help build run run-worker test cover lint migrate migrate-down up down logs

help: ## Показать список команд
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(firstword $(MAKEFILE_LIST)) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-14s %s\n", $$1, $$2}'

build: ## Собрать server и worker в bin/
	go build -o $(BIN_DIR)/server ./cmd/server
	go build -o $(BIN_DIR)/worker ./cmd/worker

run: ## Запустить server локально
	go run ./cmd/server

run-worker: ## Запустить worker локально
	go run ./cmd/worker

test: ## Запустить тесты
	go test -count=1 ./...

cover: ## Тесты с отчётом о покрытии
	go test -count=1 -coverprofile=$(COVER_FILE) ./...
	go tool cover -func=$(COVER_FILE)

lint: ## Запустить golangci-lint
	golangci-lint run ./...

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
