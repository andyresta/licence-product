# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/licenseserver ./cmd/licenseserver

# ---- runtime stage ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget

WORKDIR /app
COPY --from=build /out/licenseserver ./licenseserver
COPY migrations ./migrations

RUN adduser -D -H -u 10001 appuser
USER appuser

EXPOSE 7575
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -q -O - http://127.0.0.1:${APP_PORT:-7575}/healthz || exit 1

ENTRYPOINT ["./licenseserver"]
