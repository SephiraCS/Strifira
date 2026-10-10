FROM docker.io/library/node:24-alpine AS builder
WORKDIR /app
COPY package*.json ./
RUN npm ci --no-audit --no-fund
COPY . .
ARG PUBLIC_DEMO_MODE=false
ARG PUBLIC_DEMO_USERS
ENV PUBLIC_DEMO_MODE=$PUBLIC_DEMO_MODE
ENV PUBLIC_DEMO_USERS=$PUBLIC_DEMO_USERS
RUN npm run build

# Same Caddy build as the root Dockerfile.
FROM docker.io/library/golang:1.27.2-alpine AS caddy-builder
RUN go install github.com/caddyserver/xcaddy/cmd/xcaddy@v0.4.7
RUN xcaddy build v2.11.7 \
    --replace golang.org/x/net=golang.org/x/net@v0.60.0

FROM docker.io/library/alpine:3.24
RUN apk upgrade --no-cache
COPY --from=caddy-builder /go/caddy /usr/bin/caddy
COPY --from=builder /app/build /srv
COPY Caddyfile /etc/caddy/Caddyfile
RUN caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
EXPOSE 3000
CMD ["caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]
