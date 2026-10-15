FROM docker.io/library/node:22.23.1-bookworm-slim@sha256:6c74791e557ce11fc957704f6d4fe134a7bc8d6f5ca4403205b2966bd488f6b3 AS node-stage

FROM docker.io/library/ubuntu:24.04

ARG TARGETARCH
ARG DEBIAN_FRONTEND=noninteractive
ARG CODE_SERVER_VERSION=4.141.0
ARG CODE_SERVER_AMD64_SHA256=f384beb4f3ba207e0bca27d0dbd36dd825d5aa909d78c6d4baa8703eaf6b77ba
ARG CODE_SERVER_ARM64_SHA256=0bca33a9b01180ad88f0b8969a8e34ae1c76417df471697e7cd65c20ef5dd60d
ARG TTYD_VERSION=1.7.7
ARG TTYD_AMD64_SHA256=8a217c968aba172e0dbf3f34447218dc015bc4d5e59bf51db2f2cd12b7be4f55
ARG TTYD_ARM64_SHA256=b38acadd89d1d396a0f5649aa52c539edbad07f4bc7348b27b4f4b7219dd4165
ARG NPM_VERSION=12.0.1
ARG CLAUDE_CODE_VERSION=2.1.218
ARG OPENCODE_VERSION=1.18.4
ARG CODEX_VERSION=0.145.0
ARG BRACE_EXPANSION_VERSION=5.0.11
ARG IP_ADDRESS_VERSION=10.3.1
ARG SHELL_QUOTE_VERSION=1.11.0
ARG BASIC_FTP_VERSION=6.2.1
ARG JS_YAML_VERSION=4.3.2
ARG TAR_VERSION=7.5.21
ARG UNDICI_VERSION=7.29.1

COPY --from=node-stage /usr/local /usr/local
COPY common/strifira-service-entrypoint /usr/local/bin/strifira-service-entrypoint
COPY ide/start.sh /usr/local/bin/strifira-ide-start
COPY ide/shell.sh /usr/local/bin/strifira-shell
COPY ide/terminal-session.sh /usr/local/bin/strifira-terminal-session
COPY ide/command-event.js /usr/local/lib/strifira-command-event.js
COPY ide/tmux.conf /etc/tmux.conf
# VS Code's npm support extension is not the npm CLI package; keep its manifest identity unambiguous.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl git ripgrep sudo tmux util-linux xz-utils \
    && case "${TARGETARCH}" in \
         amd64) package_arch=amd64; code_server_sha="$CODE_SERVER_AMD64_SHA256"; ttyd_arch=x86_64; ttyd_sha="$TTYD_AMD64_SHA256" ;; \
         arm64) package_arch=arm64; code_server_sha="$CODE_SERVER_ARM64_SHA256"; ttyd_arch=aarch64; ttyd_sha="$TTYD_ARM64_SHA256" ;; \
         *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
       esac \
    && curl -fsSL "https://github.com/coder/code-server/releases/download/v${CODE_SERVER_VERSION}/code-server_${CODE_SERVER_VERSION}_${package_arch}.deb" -o /tmp/code-server.deb \
    && echo "${code_server_sha}  /tmp/code-server.deb" | sha256sum -c - \
    && apt-get install -y --no-install-recommends /tmp/code-server.deb \
    && curl -fsSL "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/ttyd.${ttyd_arch}" -o /usr/local/bin/ttyd \
    && echo "${ttyd_sha}  /usr/local/bin/ttyd" | sha256sum -c - \
    && chmod 0755 /usr/local/bin/ttyd \
    && npm install -g "npm@${NPM_VERSION}" \
    && npm install -g --allow-scripts=@anthropic-ai/claude-code "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
    && npm install -g --allow-scripts=opencode-ai "opencode-ai@${OPENCODE_VERSION}" \
    && npm install -g "@openai/codex@${CODEX_VERSION}" \
    && for pair in "brace-expansion@${BRACE_EXPANSION_VERSION}" "ip-address@${IP_ADDRESS_VERSION}" "js-yaml@${JS_YAML_VERSION}" "tar@${TAR_VERSION}" "undici@${UNDICI_VERSION}"; do npm install --prefix /tmp/npmpatch --ignore-scripts --package-lock=false "$pair" >/dev/null; done \
    && for name in brace-expansion ip-address tar undici; do src="/tmp/npmpatch/node_modules/$name"; find /usr/local/lib/node_modules /usr/lib/code-server -type d -path "*/node_modules/$name" | while read -r dst; do rm -rf "$dst"; cp -a "$src" "$dst"; done; done \
    && find /usr/local/lib/node_modules -type d -path "*/node_modules/js-yaml" | while read -r dst; do rm -rf "$dst"; cp -a /tmp/npmpatch/node_modules/js-yaml "$dst"; done \
    && npm install --prefix /tmp/code-server-patches --ignore-scripts --package-lock=false \
         "shell-quote@${SHELL_QUOTE_VERSION}" "tar@${TAR_VERSION}" \
         "basic-ftp@${BASIC_FTP_VERSION}" \
    && rm -rf /usr/lib/code-server/node_modules/basic-ftp \
         /usr/lib/code-server/lib/vscode/node_modules/shell-quote \
         /usr/lib/code-server/lib/vscode/node_modules/tar \
    && cp -a /tmp/code-server-patches/node_modules/basic-ftp /usr/lib/code-server/node_modules/ \
    && cp -a /tmp/code-server-patches/node_modules/shell-quote /usr/lib/code-server/lib/vscode/node_modules/ \
    && cp -a /tmp/code-server-patches/node_modules/tar /usr/lib/code-server/lib/vscode/node_modules/ \
    && node -e 'const fs=require("fs"),path="/usr/lib/code-server/lib/vscode/extensions/npm/package.json",manifest=JSON.parse(fs.readFileSync(path));manifest.name="npm-support";fs.writeFileSync(path,JSON.stringify(manifest,null,2)+"\n")' \
    && rm -rf /root/.npm /tmp/npmpatch /tmp/code-server-patches /var/lib/apt/lists/* \
    && rm -f /tmp/code-server.deb \
    && rm -f /etc/ssl/private/ssl-cert-snakeoil.key /etc/ssl/certs/ssl-cert-snakeoil.pem \
    && mkdir -p /home/strifira \
    && chown 1000:1000 /home/strifira \
    && chmod 0755 /usr/local/bin/strifira-service-entrypoint /usr/local/bin/strifira-ide-start /usr/local/bin/strifira-shell /usr/local/bin/strifira-terminal-session \
    && code-server --version \
    && node --version \
    && npm --version \
    && opencode --version \
    && claude --version \
    && codex --version \
    && ttyd --version \
    && rm -rf /root/.config/code-server \
    && user_name=$(getent passwd 1000 | cut -d: -f1) \
    && printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$user_name" > /etc/sudoers.d/strifira-builder \
    && chmod 0440 /etc/sudoers.d/strifira-builder

USER 1000:1000
ENV HOME=/home/strifira
EXPOSE 8080 7681
CMD ["/usr/local/bin/strifira-ide-start"]
