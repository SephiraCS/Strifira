ARG GO_IMAGE=docker.io/library/golang:1.27.1-alpine
ARG NODE_IMAGE=docker.io/library/node:22.23.1-alpine3.23
FROM ${GO_IMAGE} AS go-stage

FROM ${NODE_IMAGE}

ARG PROVIDER
ARG NPM_VERSION=12.0.1
ARG CLAUDE_CODE_VERSION=2.1.218
ARG OPENCODE_VERSION=1.18.4
ARG CODEX_VERSION=0.145.0

RUN apk upgrade --no-cache && apk add --no-cache bash ca-certificates curl git make openssh-client \
    && npm install -g "npm@${NPM_VERSION}" \
    && case "$PROVIDER" in \
         claude-code) npm install -g --allow-scripts=@anthropic-ai/claude-code "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" ;; \
         opencode) npm install -g --allow-scripts=opencode-ai "opencode-ai@${OPENCODE_VERSION}" ;; \
         codex) npm install -g "@openai/codex@${CODEX_VERSION}" ;; \
         *) echo "unsupported provider: $PROVIDER" >&2; exit 1 ;; \
       esac \
    && for pair in "brace-expansion@5.0.11" "ip-address@10.3.1" "js-yaml@4.3.2" "tar@7.5.21" "undici@7.29.1"; do npm install --prefix /tmp/npmpatch --ignore-scripts --package-lock=false "$pair" >/dev/null; done \
    && for name in brace-expansion ip-address js-yaml tar undici; do src="/tmp/npmpatch/node_modules/$name"; find /usr/local/lib/node_modules -type d -path "*/node_modules/$name" | while read -r dst; do rm -rf "$dst"; cp -a "$src" "$dst"; done; done \
    && rm -rf /tmp/npmpatch /root/.npm \
    && case "$PROVIDER" in \
         claude-code) claude --version ;; \
         opencode) opencode --version ;; \
         codex) codex --version ;; \
       esac \
    && npm --version

COPY --from=go-stage /usr/local/go /usr/local/go
ENV GOPATH=/home/ku/go \
    GOCACHE=/home/ku/.cache/go-build \
    GOMODCACHE=/home/ku/go/pkg/mod \
    PATH="/usr/local/go/bin:${PATH}"

COPY common/ku-agent-runner /usr/local/bin/ku-agent-runner
RUN chmod 0755 /usr/local/bin/ku-agent-runner \
    && mkdir -p /home/ku /home/ku/go /home/ku/.cache/go-build \
    && chown -R 1000:1000 /home/ku

USER 1000:1000
ENV HOME=/home/ku
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/ku-agent-runner"]
