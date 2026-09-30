FROM docker.io/library/alpine:3.23 AS builder
WORKDIR /app
RUN apk add --no-cache nodejs npm
COPY package*.json ./
RUN npm ci
COPY . .
ARG PUBLIC_DEMO_MODE=false
ARG PUBLIC_DEMO_USERS
ENV PUBLIC_DEMO_MODE=$PUBLIC_DEMO_MODE
ENV PUBLIC_DEMO_USERS=$PUBLIC_DEMO_USERS
RUN npm run build

# Same Caddy build as the root Dockerfile.
FROM docker.io/library/golang:1.27.1-alpine AS caddy-builder
RUN go install github.com/caddyserver/xcaddy/cmd/xcaddy@v0.4.7
RUN xcaddy build v2.11.4 \
    --replace golang.org/x/crypto=golang.org/x/crypto@v0.55.0 \
    --replace google.golang.org/grpc=google.golang.org/grpc@v1.83.2

FROM docker.io/library/alpine:3.23
RUN apk update && apk upgrade --no-cache
COPY --from=caddy-builder /go/caddy /usr/bin/caddy
COPY --from=builder /app/build /srv
COPY Caddyfile /etc/caddy/Caddyfile
EXPOSE 3000
CMD ["caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]
