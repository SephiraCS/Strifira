# Stage 1: Build UI
FROM docker.io/library/node:24-alpine AS ui-builder
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
# The commit is baked into the binary and reported by /health, so CI can
# verify that the rolled instance runs the commit it deployed.
ARG KU_VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/ku/ku-backend/internal/handler.Version=${KU_VERSION}" \
    -o server ./cmd/server

# Stage 4: Final image
FROM docker.io/library/alpine:3.24
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates su-exec && \
    addgroup -S ku && adduser -S -G ku -h /app/.home ku
WORKDIR /app
COPY --from=caddy-builder /go/caddy /usr/bin/caddy
COPY --from=be-builder /app/server .
COPY --from=be-builder /app/migrations ./migrations
COPY --from=ui-builder /app/build /srv
# Readable by the unprivileged user whatever the checkout's umask was.
RUN chmod -R a+rX /app/server /app/migrations /srv && \
    mkdir -p /app/uploads /app/.home && chown ku:ku /app/uploads /app/.home

COPY <<'EOF' /etc/caddy/Caddyfile
{
	# Bound connection draining on SIGTERM: without it a long-lived stream
	# or idle keep-alive connection could hold Caddy (and the container)
	# open indefinitely. The start script kills anything still running
	# after its own, slightly longer, deadline.
	grace_period 20s

	# Runtime logs as JSON so Caddy and the Go API share one parseable
	# format in the container console. reverse_proxy logs stream to their
	# own sink below: its WARN "aborting with incomplete response" fires
	# every time a client drops a long-poll mid-response (the MCP
	# subscriptions/listen reconnect cycle) and is expected, not an
	# upstream failure — only genuine proxy errors reach the console.
	log default {
		format json
		exclude http.handlers.reverse_proxy
	}
	log reverse_proxy {
		format json
		level ERROR
		include http.handlers.reverse_proxy
	}
}

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
		reverse_proxy localhost:8080 {
			# The edge proxy (on alpha: caddy-edge through the incus proxy
			# devices) forwards the real client IP in X-Forwarded-For.
			# Without this, Caddy treats the connection as untrusted and
			# overwrites that header with its own view (127.0.0.1), so the
			# API's request logs lose the client address. Loopback is the
			# only permitted source of trusted forwarding: connections
			# from anywhere else still get XFF rewritten to their real
			# peer address.
			trusted_proxies 127.0.0.1
		}
	}

	# MCP endpoint (stateless streamable HTTP, API-key auth in the app)
	handle /mcp* {
		reverse_proxy localhost:8080 {
			trusted_proxies 127.0.0.1
		}
	}

	handle /uploads/* {
		reverse_proxy localhost:8080 {
			trusted_proxies 127.0.0.1
		}
	}

	handle /health {
		reverse_proxy localhost:8080 {
			trusted_proxies 127.0.0.1
		}
	}

	handle /ready {
		reverse_proxy localhost:8080 {
			trusted_proxies 127.0.0.1
		}
	}

	# Hashed build assets never change, so browsers keep them for a year. A
	# missing one (a tab still on a previous deploy) is a real 404, not the
	# SPA shell, so the client can tell and reload.
	handle /_app/immutable/* {
		root * /srv
		header Cache-Control "public, max-age=31536000, immutable" {
			match {
				status 2xx
			}
		}
		file_server
	}

	# Everything else (the SPA shell, version.json, the service worker)
	# revalidates on every load so a deploy shows up immediately.
	handle {
		root * /srv
		header Cache-Control no-cache
		try_files {path} /index.html
		file_server
	}
}
EOF
RUN caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile

# Runs as root only long enough to read the mounted env file (root-owned on
# alpha) and hand the uploads volume to the ku user; migrations, the API and
# Caddy then run unprivileged. The uploads tree predates the non-root image,
# so it is re-owned on start; an older (root) image still writes it fine.
#
# The API and Caddy run side by side, and the container must not outlive
# either: busybox sh has no `wait -n`, so the script polls both and stops the
# container (non-zero) as soon as one exits, letting the runtime restart it
# or ku-deploy roll back instead of serving 502s behind a live Caddy.
COPY <<'EOF' /app/start.sh
#!/bin/sh
set -eu

if [ -f /secrets/ku.env ]; then
	set -a
	. /secrets/ku.env
	set +a
fi

if [ "$(id -u)" = 0 ]; then
	chown -R ku:ku /app/uploads
	run_as="su-exec ku"
else
	run_as=""
fi

attempt=1
until $run_as ./server migrate up; do
	if [ "$attempt" -ge 20 ]; then
		echo "[ku] migrations failed after $attempt attempts" >&2
		exit 1
	fi
	echo "[ku] migration attempt $attempt failed; retrying in 3s" >&2
	attempt=$((attempt + 1))
	sleep 3
done

$run_as ./server &
server=$!
$run_as caddy run --config /etc/caddy/Caddyfile --adapter caddyfile &
caddy=$!

# Forward SIGTERM, give both processes a bounded grace period (the API
# drains for up to 25s, Caddy for 20s), then SIGKILL whatever is left so
# neither a shutdown nor the child-failure path can hang the container.
stop() {
	kill -TERM "$server" "$caddy" 2>/dev/null || true
	waited=0
	while [ "$waited" -lt 30 ] && { kill -0 "$server" 2>/dev/null || kill -0 "$caddy" 2>/dev/null; }; do
		sleep 1
		waited=$((waited + 1))
	done
	kill -KILL "$server" "$caddy" 2>/dev/null || true
	wait "$server" 2>/dev/null || true
	wait "$caddy" 2>/dev/null || true
}
trap 'stop; exit 143' TERM INT

while kill -0 "$server" 2>/dev/null && kill -0 "$caddy" 2>/dev/null; do
	sleep 1
done
if kill -0 "$server" 2>/dev/null; then
	echo "[ku] caddy exited; stopping the container" >&2
else
	echo "[ku] the API server exited; stopping the container" >&2
fi
stop
exit 1
EOF
RUN chmod +x /app/start.sh

ENV HOME=/app/.home XDG_CONFIG_HOME=/app/.home/config XDG_DATA_HOME=/app/.home/data
EXPOSE 3000
# Docker-format images only (scripts/ci/build-image.sh builds with
# --format docker); Incus on alpha ignores it and ku-deploy polls /health.
HEALTHCHECK --interval=30s --timeout=5s --start-period=90s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:3000/health || exit 1
ENTRYPOINT ["/app/start.sh"]
