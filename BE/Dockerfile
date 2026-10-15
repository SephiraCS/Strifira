FROM docker.io/library/golang:1.27.2-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Reported by /health; see the root Dockerfile.
ARG STRIFIRA_VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/SephiraCS/strifira-backend/internal/handler.Version=${STRIFIRA_VERSION}" \
    -o server ./cmd/server

FROM docker.io/library/alpine:3.24
RUN apk add --no-cache ca-certificates && \
    addgroup -S appgroup && adduser -S appuser -G appgroup
WORKDIR /app
COPY --from=builder /app/server .
COPY --from=builder /app/migrations ./migrations
RUN mkdir -p /app/uploads && chown -R appuser:appgroup /app
USER appuser
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD wget -q -O /dev/null "http://127.0.0.1:${PORT:-8080}/health" || exit 1
CMD ["./server"]
