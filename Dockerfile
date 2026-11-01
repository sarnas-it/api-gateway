# Сборка
FROM golang:1-alpine AS builder

ARG GIT_SHA
ARG BUILD_TIME
ARG BUILD_RUN_ID

WORKDIR /build

RUN apk add --no-cache git

# Кешируем зависимости
COPY go.mod go.sum ./
RUN go mod download

# Копируем код и собираем
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s -X code.sarnas.ru/api-gateway/internal/proxy.GitSHA=${GIT_SHA} -X code.sarnas.ru/api-gateway/internal/proxy.BuildTime=${BUILD_TIME} -X code.sarnas.ru/api-gateway/internal/proxy.BuildRunID=${BUILD_RUN_ID}" -trimpath -o api-gateway ./cmd/gateway/

FROM scratch

# GOGC=400: даём куче расти до 4x перед сборкой мусора вместо стандартных 100%.
# Реальный footprint гейтвея — единицы-десятки MB, лимит контейнера обычно
# сотни MB-несколько GB, так что памяти в избытке — а частые сборки мусора
# были заметной долей CPU на горячем пути. Замерено: +15-17% RPS под нагрузкой
# ценой роста памяти в состоянии простоя (десятки -> ~140MB). Переопределяется
# снаружи через -e GOGC=... при необходимости.
ENV GOGC=400

# Копируем сертификаты и timezone
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Копируем бинарник
COPY --from=builder /build/api-gateway /app/api-gateway

WORKDIR /app

CMD ["/app/api-gateway"]
