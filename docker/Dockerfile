# Один Dockerfile на оба бинарника: общая стадия сборки и две финальные цели.
#   docker build -f docker/Dockerfile --target server -t gophprofile-server .
#   docker build -f docker/Dockerfile --target worker -t gophprofile-worker .
# Без --target собирается server.

# Build stage: зависимости кешируются отдельным слоем, бинарники статические.
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ENV CGO_ENABLED=0 GOOS=linux
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
    && go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

# Общая база рантайма: сертификаты, часовые пояса и непривилегированный пользователь.
# HTML-шаблоны и миграции встроены в бинарники через embed.
FROM alpine:3.22 AS runtime
RUN apk --no-cache add ca-certificates tzdata \
    && adduser -D -H app
WORKDIR /app
USER app

FROM runtime AS worker
COPY --from=builder /out/worker .
ENTRYPOINT ["./worker"]

FROM runtime AS server
COPY --from=builder /out/server .
EXPOSE 8080
# wget из busybox есть в alpine: проверка для docker compose.
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["./server"]
