#!/usr/bin/env node
/**
 * Opt-in real WebRTC test: disposable PostgreSQL, backend, built SPA and
 * browser tabs. Requires Podman, Go, installed UI dependencies and Chromium
 * (`cd UI && npx playwright install chromium`). No deployed services or
 * application .env files are read. Container registry rewrites/auth follow
 * the operator's existing Podman configuration.
 *
 * node scripts/check-screen-share-live.mjs            # build UI, then test
 * node scripts/check-screen-share-live.mjs --skip-build # use existing UI/build
 */
import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { createReadStream } from "node:fs";
import { access, mkdtemp, rm, stat, writeFile } from "node:fs/promises";
import http from "node:http";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const kuDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const uiDir = path.join(kuDir, "UI");
const beDir = path.join(kuDir, "BE");
const skipBuild = process.argv.includes("--skip-build");
if (process.argv.some((arg, index) => index > 1 && arg !== "--skip-build")) {
  throw new Error(
    "Usage: node scripts/check-screen-share-live.mjs [--skip-build]",
  );
}

// Keep toolchain/runtime settings, never inherit Ku/Forgejo/cloud settings.
const toolEnv = Object.fromEntries(
  [
    "PATH",
    "HOME",
    "USER",
    "TMPDIR",
    "XDG_CONFIG_HOME",
    "XDG_CACHE_HOME",
    "XDG_RUNTIME_DIR",
    "SSL_CERT_FILE",
    "NIX_SSL_CERT_FILE",
    "LD_LIBRARY_PATH",
    "GOPATH",
    "GOMODCACHE",
    "GOCACHE",
    "GOTOOLCHAIN",
    "CGO_ENABLED",
    "PLAYWRIGHT_BROWSERS_PATH",
    "REGISTRY_AUTH_FILE",
    "CONTAINERS_REGISTRIES_CONF",
  ]
    .filter((key) => process.env[key] !== undefined)
    .map((key) => [key, process.env[key]]),
);
const tempDir = await mkdtemp(path.join(os.tmpdir(), "ku-screen-share-live-"));
const containerName = `ku-screen-share-live-${randomUUID()}`;
const children = new Set();
const sockets = new Set();
let containerCreated = false;
let server;
let interrupted = false;

function start(command, args, options = {}) {
  const child = spawn(command, args, {
    env: toolEnv,
    stdio: "inherit",
    detached: process.platform !== "win32",
    ...options,
  });
  children.add(child);
  child.once("exit", () => children.delete(child));
  child.once("error", () => children.delete(child));
  return child;
}

function terminate(child, signal) {
  if (!child.pid) return;
  try {
    // Terminate only this launcher's dedicated child process group,
    // including Vite/Playwright subprocesses on interruption.
    if (process.platform === "win32") child.kill(signal);
    else process.kill(-child.pid, signal);
  } catch (error) {
    if (error.code !== "ESRCH") throw error;
  }
}

function completed(child) {
  return new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      if (code === 0) resolve();
      else
        reject(
          new Error(
            `Process failed (${signal ?? code}): ${child.spawnargs.join(" ")}`,
          ),
        );
    });
  });
}

async function run(command, args, options) {
  await completed(start(command, args, options));
}

async function capture(command, args) {
  const child = start(command, args, { stdio: ["ignore", "pipe", "inherit"] });
  let output = "";
  child.stdout.on("data", (data) => {
    output += data;
  });
  await completed(child);
  return output.trim();
}

async function unusedPort() {
  const reservation = net.createServer();
  await new Promise((resolve, reject) => {
    reservation.once("error", reject);
    reservation.listen(0, "127.0.0.1", resolve);
  });
  const port = reservation.address().port;
  await new Promise((resolve) => reservation.close(resolve));
  return port;
}

async function waitUntil(check, description, timeout = 45_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (interrupted) throw new Error("Interrupted");
    if (await check()) return;
    await new Promise((resolve) => setTimeout(resolve, 300));
  }
  throw new Error(`Timed out waiting for ${description}`);
}

const mime = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json",
  ".webmanifest": "application/manifest+json",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".webp": "image/webp",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
  ".ico": "image/x-icon",
};

// Serve the actual static build and transparently proxy both HTTP and the
// workspace WebSocket. No API, signaling or peer connection is mocked.
async function serveBuild(apiPort) {
  const buildDir = path.join(uiDir, "build");
  await access(path.join(buildDir, "index.html"));
  server = http.createServer(async (request, response) => {
    if (
      request.url.startsWith("/api/") ||
      request.url.startsWith("/uploads/")
    ) {
      const upstream = http.request(
        {
          hostname: "127.0.0.1",
          port: apiPort,
          path: request.url,
          method: request.method,
          headers: request.headers,
        },
        (upstreamResponse) => {
          response.writeHead(
            upstreamResponse.statusCode,
            upstreamResponse.headers,
          );
          upstreamResponse.pipe(response);
        },
      );
      upstream.on("error", () => {
        if (!response.headersSent) response.writeHead(502);
        response.end("Local backend unavailable");
      });
      request.pipe(upstream);
      return;
    }
    try {
      const pathname = decodeURIComponent(
        new URL(request.url, "http://127.0.0.1").pathname,
      );
      let filename = path.resolve(buildDir, `.${pathname}`);
      if (
        !filename.startsWith(`${buildDir}${path.sep}`) &&
        filename !== buildDir
      ) {
        response.writeHead(403).end();
        return;
      }
      const file = await stat(filename).catch(() => null);
      if (!file?.isFile()) filename = path.join(buildDir, "index.html");
      response.writeHead(200, {
        "Content-Type":
          mime[path.extname(filename)] ?? "application/octet-stream",
        "Cache-Control": "no-store",
      });
      createReadStream(filename)
        .on("error", () => response.destroy())
        .pipe(response);
    } catch {
      response.writeHead(400).end();
    }
  });
  server.on("connection", (socket) => {
    sockets.add(socket);
    socket.once("close", () => sockets.delete(socket));
  });
  server.on("upgrade", (request, socket, head) => {
    if (!/^\/api\/workspaces\/[^/]+\/ws(?:\?|$)/.test(request.url)) {
      socket.destroy();
      return;
    }
    const upstream = net.connect(apiPort, "127.0.0.1", () => {
      let headers = `${request.method} ${request.url} HTTP/${request.httpVersion}\r\n`;
      for (let i = 0; i < request.rawHeaders.length; i += 2) {
        headers += `${request.rawHeaders[i]}: ${request.rawHeaders[i + 1]}\r\n`;
      }
      upstream.write(`${headers}\r\n`);
      if (head.length) upstream.write(head);
      socket.pipe(upstream).pipe(socket);
    });
    sockets.add(upstream);
    upstream.once("close", () => {
      sockets.delete(upstream);
      socket.destroy();
    });
    upstream.on("error", () => socket.destroy());
    socket.once("close", () => upstream.destroy());
    socket.on("error", () => upstream.destroy());
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  return `http://127.0.0.1:${server.address().port}`;
}

async function cleanup() {
  for (const socket of sockets) socket.destroy();
  if (server) await new Promise((resolve) => server.close(resolve));
  for (const child of children) terminate(child, "SIGTERM");
  const deadline = Date.now() + 5_000;
  while (children.size && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  for (const child of children) terminate(child, "SIGKILL");
  if (containerCreated)
    await run("podman", ["rm", "--force", "--ignore", containerName]);
  await rm(tempDir, { recursive: true, force: true });
}

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.once(signal, () => {
    interrupted = true;
    for (const child of children) terminate(child, "SIGTERM");
  });
}

try {
  if (!skipBuild) {
    // Vite normally reads ku/.env through envDir. Override it with an
    // empty directory for this local build instead of loading secrets.
    const buildConfig = path.join(tempDir, "vite-live.config.mjs");
    await writeFile(
      buildConfig,
      `import base from ${JSON.stringify(pathToFileURL(path.join(uiDir, "vite.config.ts")).href)};\nexport default { ...base, envDir: ${JSON.stringify(tempDir)} };\n`,
    );
    await run("npm", ["run", "build", "--", "--config", buildConfig], {
      cwd: uiDir,
    });
  }
  console.log(
    "Starting disposable PostgreSQL using existing Podman registry configuration.",
  );
  // Loopback-only random publish; all data disappears with this container.
  containerCreated = true;
  await run("podman", [
    "run",
    "--detach",
    "--rm",
    "--name",
    containerName,
    "--label",
    "io.ku.test=screen-share-live",
    "--publish",
    "127.0.0.1::5432",
    "--tmpfs",
    "/var/lib/postgresql/data:rw",
    "--env",
    "POSTGRES_HOST_AUTH_METHOD=trust",
    "--env",
    "POSTGRES_DB=ku_live",
    "docker.io/library/postgres:17-alpine",
  ]);
  const dbMapping = await capture("podman", [
    "port",
    containerName,
    "5432/tcp",
  ]);
  const dbPort = Number(dbMapping.match(/^127\.0\.0\.1:(\d+)$/)?.[1]);
  if (!dbPort)
    throw new Error(`Unexpected PostgreSQL port mapping: ${dbMapping}`);
  await waitUntil(async () => {
    try {
      await run(
        "podman",
        [
          "exec",
          containerName,
          "pg_isready",
          "-U",
          "postgres",
          "-d",
          "ku_live",
        ],
        { stdio: "ignore" },
      );
      return true;
    } catch {
      return false;
    }
  }, "disposable PostgreSQL");
  const databaseURL = `postgres://postgres@127.0.0.1:${dbPort}/ku_live?sslmode=disable`;
  const binary = path.join(tempDir, "ku-server");
  await run("go", ["build", "-o", binary, "./cmd/server"], { cwd: beDir });
  await run(binary, ["migrate", "up"], {
    cwd: beDir,
    env: { ...toolEnv, DATABASE_URL: databaseURL },
  });
  const apiPort = await unusedPort();
  const baseURL = await serveBuild(apiPort);
  const logs = [];
  const backend = start(binary, [], {
    cwd: beDir,
    stdio: ["ignore", "pipe", "pipe"],
    env: {
      ...toolEnv,
      PORT: String(apiPort),
      DATABASE_URL: databaseURL,
      // Config reserves Redis; the current server never uses it.
      REDIS_URL: "redis://127.0.0.1:1",
      JWT_SECRET: randomUUID() + randomUUID(),
      FRONTEND_URL: baseURL,
      ENVIRONMENT: "development",
      STORAGE_TYPE: "local",
      STORAGE_LOCAL_DIR: path.join(tempDir, "uploads"),
      DEV_MACHINES_ENABLED: "false",
    },
  });
  backend.stdout.on("data", (data) => {
    logs.push(data.toString());
  });
  backend.stderr.on("data", (data) => {
    logs.push(data.toString());
  });
  await waitUntil(async () => {
    if (backend.exitCode !== null || backend.signalCode)
      throw new Error(`Backend stopped:\n${logs.join("")}`);
    try {
      return (await fetch(`http://127.0.0.1:${apiPort}/ready`)).ok;
    } catch {
      return false;
    }
  }, "isolated backend");
  console.log(`Testing real WebRTC against disposable Ku at ${baseURL}`);
  try {
    await run(
      process.execPath,
      [
        path.join(uiDir, "node_modules/@playwright/test/cli.js"),
        "test",
        "--config",
        "playwright.integration.config.ts",
      ],
      {
        cwd: uiDir,
        env: { ...toolEnv, KU_LIVE_ISOLATED: "1", KU_LIVE_BASE_URL: baseURL },
      },
    );
  } catch (error) {
    const logFile = path.join(uiDir, "test-results/integration/backend.log");
    await writeFile(logFile, logs.join(""));
    console.error(`Backend diagnostics: ${logFile}`);
    throw error;
  }
} catch (error) {
  console.error(error.message);
  process.exitCode = interrupted ? 130 : 1;
} finally {
  await cleanup();
}
