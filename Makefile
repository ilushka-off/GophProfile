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

# --- Kubernetes (Rancher Desktop) и Helm ---
CHART_DIR       := deploy/helm/gophprofile
INFRA_CHART_DIR := deploy/helm/gophprofile-infra
K8S_DIR         := deploy/k8s
K8S_NAMESPACE   ?= gophprofile
MONITORING_NS   ?= monitoring
# Версия образов — appVersion чарта; для своей сборки: make k8s-images IMAGE_TAG=$$(git rev-parse --short HEAD).
IMAGE_TAG       ?= $(shell grep '^appVersion:' $(CHART_DIR)/Chart.yaml | tr -d '"' | awk '{print $$2}')
# Rancher Desktop с containerd: nerdctl в namespace k8s.io — образ сразу виден
# кластеру без registry. С движком dockerd (moby): make k8s-images IMAGE_BUILDER=docker.
IMAGE_BUILDER   ?= nerdctl --namespace k8s.io
HELM_VALUES     ?= $(CHART_DIR)/values-local.yaml
# Версии чартов зафиксированы: стек поднимается одинаково у всех.
INGRESS_NGINX_VERSION ?= 4.15.1
KPS_VERSION           ?= 92.17.0
LOKI_VERSION          ?= 7.5.0
ALLOY_VERSION         ?= 1.15.0
K8S_VERSION           ?= 1.30.0
# Схемы CRD (ServiceMonitor, PrometheusRule) для kubeconform.
CRD_SCHEMAS := https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json
KUBECONFORM ?= go run github.com/yannh/kubeconform/cmd/kubeconform@v0.7.0
# promtool из того же образа, что и Prometheus в docker compose.
# Правила алертов лежат в чарте (общие с Kubernetes) и монтируются отдельно.
PROMTOOL := docker run --rm -v "$(CURDIR)/$(OBSERVABILITY_DIR)/prometheus:/prometheus:ro" \
	-v "$(CURDIR)/$(CHART_DIR)/files/prometheus:/prometheus-rules:ro" -w /prometheus \
	--entrypoint promtool prom/prometheus:v3.15.0

.PHONY: help build run run-worker test test-short cover lint alerts-test migrate migrate-down up down up-observability logs \
	helm-lint helm-check k8s-images k8s-namespace k8s-ingress k8s-observability k8s-infra k8s-deploy k8s-test k8s-up k8s-down

help: ## Показать список команд
	@grep -hE '^[a-zA-Z0-9_-]+:.*## ' $(firstword $(MAKEFILE_LIST)) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-14s %s\n", $$1, $$2}'

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
	$(PROMTOOL) check rules /prometheus-rules/alerts.yml
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

# --- Kubernetes и Helm ---

helm-lint: ## helm lint обоих чартов со всеми values и брокерами
	for b in rabbitmq kafka; do \
		helm lint $(CHART_DIR) -f $(CHART_DIR)/values-local.yaml --set broker.type=$$b && \
		helm lint $(INFRA_CHART_DIR) --set broker.type=$$b || exit 1; \
	done
	helm lint $(CHART_DIR) -f $(CHART_DIR)/values-prod.yaml

helm-check: helm-lint ## helm lint + kubeconform отрендеренных манифестов (с CRD мониторинга и без)
	for b in rabbitmq kafka; do \
		helm template gophprofile $(CHART_DIR) -n $(K8S_NAMESPACE) -f $(CHART_DIR)/values-local.yaml --set broker.type=$$b \
			--api-versions monitoring.coreos.com/v1 \
			| $(KUBECONFORM) -strict -summary -kubernetes-version $(K8S_VERSION) -schema-location default -schema-location '$(CRD_SCHEMAS)' || exit 1; \
		helm template gophprofile-infra $(INFRA_CHART_DIR) -n $(K8S_NAMESPACE) --set broker.type=$$b \
			--api-versions monitoring.coreos.com/v1 \
			| $(KUBECONFORM) -strict -summary -kubernetes-version $(K8S_VERSION) -schema-location default -schema-location '$(CRD_SCHEMAS)' || exit 1; \
	done
	helm template gophprofile $(CHART_DIR) -n $(K8S_NAMESPACE) -f $(CHART_DIR)/values-prod.yaml \
		| $(KUBECONFORM) -strict -summary -kubernetes-version $(K8S_VERSION)
	$(KUBECONFORM) -strict -summary -kubernetes-version $(K8S_VERSION) $(K8S_DIR)/namespace.yaml $(K8S_DIR)/observability/jaeger.yaml

k8s-images: ## Собрать образы server и worker с тегом IMAGE_TAG (nerdctl/docker)
	$(IMAGE_BUILDER) build -f docker/Dockerfile --target server --build-arg VERSION=$(IMAGE_TAG) -t gophprofile/server:$(IMAGE_TAG) .
	$(IMAGE_BUILDER) build -f docker/Dockerfile --target worker --build-arg VERSION=$(IMAGE_TAG) -t gophprofile/worker:$(IMAGE_TAG) .

k8s-namespace: ## Namespace gophprofile с Pod Security restricted
	kubectl apply -f $(K8S_DIR)/namespace.yaml

k8s-ingress: ## ingress-nginx (в Rancher Desktop Traefik нужно отключить)
	helm upgrade --install ingress-nginx ingress-nginx --repo https://kubernetes.github.io/ingress-nginx \
		--version $(INGRESS_NGINX_VERSION) -n ingress-nginx --create-namespace --wait

k8s-observability: ## kube-prometheus-stack, Loki, Alloy, Jaeger в namespace monitoring
	helm upgrade --install kps kube-prometheus-stack --repo https://prometheus-community.github.io/helm-charts \
		--version $(KPS_VERSION) -n $(MONITORING_NS) --create-namespace -f $(K8S_DIR)/observability/kps-values.yaml --wait --timeout 10m
	helm upgrade --install loki loki --repo https://grafana.github.io/helm-charts \
		--version $(LOKI_VERSION) -n $(MONITORING_NS) -f $(K8S_DIR)/observability/loki-values.yaml --wait --timeout 10m
	helm upgrade --install alloy alloy --repo https://grafana.github.io/helm-charts \
		--version $(ALLOY_VERSION) -n $(MONITORING_NS) -f $(K8S_DIR)/observability/alloy-values.yaml --wait
	kubectl apply -f $(K8S_DIR)/observability/jaeger.yaml

k8s-infra: k8s-namespace ## PostgreSQL, S3 и брокер (BROKER=rabbitmq|kafka) в namespace gophprofile
	helm upgrade --install gophprofile-infra $(INFRA_CHART_DIR) -n $(K8S_NAMESPACE) --set broker.type=$(BROKER) --wait --timeout 5m

k8s-deploy: ## Приложение: миграции (хук), server, worker (BROKER=rabbitmq|kafka)
	helm upgrade --install gophprofile $(CHART_DIR) -n $(K8S_NAMESPACE) -f $(HELM_VALUES) \
		--set broker.type=$(BROKER) --set server.image.tag=$(IMAGE_TAG) --set worker.image.tag=$(IMAGE_TAG) \
		--wait --timeout 5m

k8s-test: ## helm test: /health через Service
	helm test gophprofile -n $(K8S_NAMESPACE) --logs

k8s-up: k8s-ingress k8s-observability k8s-images k8s-infra k8s-deploy ## Поднять всё в Rancher Desktop (BROKER=rabbitmq|kafka)
	@echo "Добавьте '127.0.0.1 avatars.local' в hosts и откройте http://avatars.local/web/upload"

k8s-down: ## Удалить приложение и зависимости (стек мониторинга и ingress-nginx остаются)
	-helm uninstall gophprofile -n $(K8S_NAMESPACE) --wait
	-helm uninstall gophprofile-infra -n $(K8S_NAMESPACE) --wait
	# Хуки (Secret, ServiceAccount) и PVC helm uninstall не удаляет — уходят вместе с namespace.
	kubectl delete namespace $(K8S_NAMESPACE) --ignore-not-found
