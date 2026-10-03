# Сборка образа для бота.
FROM golang:1.22-alpine AS builder

WORKDIR /src

# Сначала зависимости — так кэш слоёв не ломается при правках кода.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Статический бинарник без cgo: запускается в scratch/alpine.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/bot \
    ./cmd/bot

# Финальный слой — минимальный alpine с сертификатами и таймзоной.
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 app

WORKDIR /app

COPY --from=builder /out/bot /app/bot

# Данные лежат в томе, чтобы переживали пересборку контейнера.
RUN mkdir -p /app/data && chown -R app:app /app
VOLUME ["/app/data"]

USER app

ENV DATA_FILE=/app/data/users.json

ENTRYPOINT ["/app/bot"]
