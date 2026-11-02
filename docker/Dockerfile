# Один Dockerfile на оба бинарника: общая стадия сборки и две финальные цели.
#   docker build -f docker/Dockerfile --target server --build-arg VERSION=0.3.0 -t gophprofile/server:0.3.0 .
#   docker build -f docker/Dockerfile --target worker --build-arg VERSION=0.3.0 -t gophprofile/worker:0.3.0 .
# Без --target собирается server. Тег образа — версия или короткий git SHA,
# не latest: с latest helm upgrade не видит изменений и поды не перезапускаются.

# Build stage: зависимости кешируются отдельным слоем, бинарники статические
# (CGO_ENABLED=0: обработка картинок на чистом Go, libvips не нужен).
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
ENV CGO_ENABLED=0 GOOS=linux
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/server ./cmd/server \
    && go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/worker ./cmd/worker

# Рантайм — distroless static nonroot: только сертификаты, tzdata и
# пользователь nonroot (UID/GID 65532). Нет shell и пакетного менеджера, поэтому
# healthcheck docker compose — подкоманда самого бинарника (server healthcheck).
# HTML-шаблоны и миграции встроены в бинарники через embed.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
WORKDIR /app
# Числовой UID: kubelet проверяет runAsNonRoot только по числу.
USER 65532:65532

FROM runtime AS worker
COPY --from=builder /out/worker /app/worker
# /metrics, /livez и /health (METRICS_ADDR).
EXPOSE 9091
ENTRYPOINT ["/app/worker"]

FROM runtime AS server
COPY --from=builder /out/server /app/server
# 8080 — API и веб, 9091 — /metrics (METRICS_ADDR).
EXPOSE 8080 9091
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
    CMD ["/app/server", "healthcheck"]
ENTRYPOINT ["/app/server"]
