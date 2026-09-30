# Stage 1: Build UI
FROM docker.io/library/node:22-alpine AS ui-builder
WORKDIR /app
COPY UI/package*.json ./
RUN npm ci --no-audit --no-fund --prefer-offline
COPY UI/ .
RUN npm run build

# Stage 2: Build Caddy from source with patched Go. Versions are pinned so
# the build is reproducible and a warm Go build cache makes it a relink.
# x/crypto and grpc are lifted past their advisories; x/text needs no pin:
# Caddy v2.11.4 already requires v0.41.0 (a v0.39.0 replace downgraded it).
FROM docker.io/library/golang:1.27.1-alpine AS caddy-builder
RUN go install github.com/caddyserver/xcaddy/cmd/xcaddy@v0.4.7
RUN xcaddy build v2.11.4 \
    --replace golang.org/x/crypto=golang.org/x/crypto@v0.55.0 \
    --replace google.golang.org/grpc=google.golang.org/grpc@v1.83.2

# Stage 3: Build backend
FROM docker.io/library/golang:1.27.1-alpine AS be-builder
WORKDIR /app
COPY BE/go.mod BE/go.sum ./
RUN go mod download
COPY BE/ .
RUN CGO_ENABLED=0 go build -trimpath -o server ./cmd/server

# Stage 4: Final image
FROM docker.io/library/alpine:3.23
RUN apk update && apk upgrade --no-cache && apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=caddy-builder /go/caddy /usr/bin/caddy
COPY --from=be-builder /app/server .
COPY --from=be-builder /app/migrations ./migrations
COPY --from=ui-builder /app/build /srv
RUN mkdir -p /app/uploads

COPY <<'EOF' /etc/caddy/Caddyfile
:3000 {
	header {
		X-Content-Type-Options nosniff
		X-Frame-Options DENY
		Referrer-Policy strict-origin-when-cross-origin
	}

	# Proxied paths must come before the SPA fallback: with the previous
	# top-level try_files, Caddy's directive order ran the rewrite before
	# these handle blocks, so /api/* and /health were rewritten to
	# /index.html and every API call got the SPA instead of the backend.
	# Same structure as selfhosting/Caddyfile.
	handle /api/* {
		reverse_proxy localhost:8080
	}

	# MCP endpoint (stateless streamable HTTP, API-key auth in the app)
	handle /mcp* {
		reverse_proxy localhost:8080
	}

	handle /uploads/* {
		reverse_proxy localhost:8080
	}

	handle /health {
		reverse_proxy localhost:8080
	}

	handle /ready {
		reverse_proxy localhost:8080
	}

	handle {
		root * /srv
		try_files {path} /index.html
		file_server
	}
}
EOF

# Sources an optional mounted env file, applies migrations (retried while the
# database is still coming up), then runs the API behind Caddy.
COPY <<'EOF' /app/start.sh
#!/bin/sh
set -eu

if [ -f /secrets/ku.env ]; then
	set -a
	. /secrets/ku.env
	set +a
fi

attempt=1
until ./server migrate up; do
	if [ "$attempt" -ge 20 ]; then
		echo "[ku] migrations failed after $attempt attempts" >&2
		exit 1
	fi
	echo "[ku] migration attempt $attempt failed; retrying in 3s" >&2
	attempt=$((attempt + 1))
	sleep 3
done

./server &
exec caddy run --config /etc/caddy/Caddyfile --adapter caddyfile
EOF
RUN chmod +x /app/start.sh

EXPOSE 3000
ENTRYPOINT ["/app/start.sh"]
