# syntax=docker/dockerfile:1
ARG GO_VERSION=1.22
FROM golang:${GO_VERSION}-bookworm AS builder

WORKDIR /usr/src/app

# Сначала только модули (для кеша)
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && go mod verify

# Потом весь код
COPY . .

# Сборка именно cmd/app
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -v -o /run-app ./cmd/app


FROM debian:bookworm

# (опционально) сертификаты, если делаешь HTTPS запросы (OpenRouter/Telegram)
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /run-app /usr/local/bin/run-app

CMD ["run-app"]
