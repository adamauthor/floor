# Образ для Fly.io: Go-бинарь + Claude Code CLI (для `claude -p`).
# Mac-режиму Docker не нужен. Сборка: make deploy-fly (из корня репозитория).

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /out/floor ./cmd/floor

FROM node:22-alpine
RUN npm install -g @anthropic-ai/claude-code \
 && apk add --no-cache tzdata ca-certificates
COPY --from=build /out/floor /usr/local/bin/floor
# Том Fly монтируется в /data от root, поэтому процесс работает от root.
ENV HEALTH_SOURCE=http \
    HTTP_ADDR=:8080 \
    DB_PATH=/data/floor.db
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/floor"]
