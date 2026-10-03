"""Shared isolated Ku backend/PostgreSQL fixture for opt-in live acceptance.

Each context creates and removes its own resources. No application .env or
production accounts/settings are loaded. API errors and subprocess failures
never include raw responses or secret-bearing command output.
"""

import contextlib
import hashlib
import http.client
import http.cookiejar
import json
import os
import pathlib
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid
from dataclasses import dataclass


class LiveCheckError(Exception):
    """A controlled diagnostic that never includes a response body or secret."""


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request_json(
    opener,
    base_url,
    path,
    *,
    method="GET",
    data=None,
    authorization=None,
    timeout=55,
    expected=200,
):
    headers = {"Accept": "application/json"}
    body = None
    if data is not None:
        body = json.dumps(data).encode()
        headers["Content-Type"] = "application/json"
    if authorization is not None:
        headers["Authorization"] = "Bearer " + authorization
    request = urllib.request.Request(
        base_url + path, data=body, headers=headers, method=method
    )
    try:
        response = opener.open(request, timeout=timeout)
    except urllib.error.HTTPError as error:
        if error.code != expected:
            raise LiveCheckError(
                f"{method} {path} failed with HTTP {error.code}"
            ) from None
        response = error
    except (urllib.error.URLError, OSError, http.client.HTTPException):
        raise LiveCheckError(f"{method} {path} could not reach its endpoint") from None
    with response:
        if response.status != expected:
            raise LiveCheckError(
                f"{method} {path} returned unexpected HTTP {response.status}"
            )
        try:
            content = response.read((1 << 20) + 1)
        except (OSError, http.client.HTTPException):
            raise LiveCheckError(
                f"{method} {path} could not read a complete response"
            ) from None
    if len(content) > 1 << 20:
        raise LiveCheckError(f"{method} {path} returned an oversized response")
    if expected == 204:
        return None
    try:
        return json.loads(content)
    except (ValueError, UnicodeDecodeError):
        raise LiveCheckError(f"{method} {path} returned invalid JSON") from None


def tool_environment():
    allowed = (
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
        "REGISTRY_AUTH_FILE",
        "CONTAINERS_REGISTRIES_CONF",
        # The job's isolated podman store and its containers.conf override
        # (cgroups disabled under the nested CI runner; scripts/ci/podman-store.sh).
        "CONTAINERS_STORAGE_CONF",
        "CONTAINERS_CONF_OVERRIDE",
    )
    return {name: os.environ[name] for name in allowed if name in os.environ}


def run(command, env, *, cwd=None, timeout=180, input_text=None):
    try:
        result = subprocess.run(
            command,
            env=env,
            cwd=cwd,
            capture_output=True,
            timeout=timeout,
            check=False,
            input=input_text.encode() if input_text is not None else None,
        )
    except (OSError, subprocess.TimeoutExpired):
        raise LiveCheckError(f"{command[0]} did not complete") from None
    if result.returncode != 0:
        raise LiveCheckError(f"{command[0]} failed with exit code {result.returncode}")
    return result.stdout.decode().strip()


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_ready(check, label, *, process=None):
    deadline = time.monotonic() + 45
    while time.monotonic() < deadline:
        if process is not None and process.poll() is not None:
            raise LiveCheckError(label + " stopped before readiness")
        try:
            if check():
                return
        except LiveCheckError:
            pass
        time.sleep(0.2)
    raise LiveCheckError(label + " did not become ready")


def verify_loopback_listener(port, env):
    listeners = run(["ss", "-H", "-ltn", "sport", "=", ":" + str(port)], env)
    addresses = [line.split()[3] for line in listeners.splitlines() if line.strip()]
    if addresses != ["127.0.0.1:" + str(port)]:
        raise LiveCheckError(
            "Disposable backend must bind only to 127.0.0.1; use a reviewed binary with BIND_ADDRESS support"
        )


@dataclass
class LiveFixture:
    api: urllib.request.OpenerDirector
    base_url: str
    container: str
    env: dict[str, str]
    server_sha256: str

    def sql(self, statement):
        """Run fixture-only SQL via stdin; never put SQL or secrets in argv."""
        return run(
            [
                "podman",
                "exec",
                "--interactive",
                self.container,
                "psql",
                "--no-psqlrc",
                "-v",
                "ON_ERROR_STOP=1",
                "-U",
                "postgres",
                "-d",
                "ku_live",
                "-At",
            ],
            self.env,
            input_text=statement,
        )


@contextlib.contextmanager
def disposable_backend(server_bin, be_dir, *, extra_env=None, label="isolated-live"):
    """Yield a fresh API/SQL fixture, terminating only its own child and DB.

    Extra environment can enable the behavior under test, but cannot override
    the fixture's database, loopback binding, runtime mode, or random JWT key.
    """
    be_dir = pathlib.Path(be_dir).resolve()
    env = tool_environment()
    extra_env = dict(extra_env or {})
    protected = {
        "DATABASE_URL",
        "PORT",
        "BIND_ADDRESS",
        "JWT_SECRET",
        "FRONTEND_URL",
        "REDIS_URL",
        "ENVIRONMENT",
        "STORAGE_TYPE",
        "STORAGE_LOCAL_DIR",
    }
    if protected.intersection(extra_env):
        raise LiveCheckError(
            "Extra environment must not override isolated fixture settings"
        )
    container = "ku-live-" + uuid.uuid4().hex
    backend = None
    api = None
    backend_url = None
    container_attempted = False
    with tempfile.TemporaryDirectory(prefix="ku-live-") as temporary:
        temp_dir = pathlib.Path(temporary)
        with (temp_dir / "backend.log").open("wb") as backend_log:
            try:
                binary = server_bin
                if binary is None:
                    binary = temp_dir / "ku-server"
                    run(
                        ["go", "build", "-p", "1", "-o", str(binary), "./cmd/server"],
                        {**env, "GOMAXPROCS": "2"},
                        cwd=be_dir,
                        timeout=600,
                    )
                binary = pathlib.Path(binary).resolve()
                if not binary.is_file() or not os.access(binary, os.X_OK):
                    raise LiveCheckError(
                        "Reviewed Ku backend binary is missing or not executable"
                    )
                with binary.open("rb") as server_file:
                    server_sha256 = hashlib.file_digest(
                        server_file, "sha256"
                    ).hexdigest()

                container_attempted = True
                run(
                    [
                        "podman",
                        "run",
                        "--detach",
                        "--rm",
                        "--name",
                        container,
                        "--label",
                        "io.ku.test=" + label,
                        "--publish",
                        "127.0.0.1::5432",
                        "--tmpfs",
                        "/var/lib/postgresql/data:rw",
                        "--env",
                        "POSTGRES_HOST_AUTH_METHOD=trust",
                        "--env",
                        "POSTGRES_DB=ku_live",
                        "docker.io/library/postgres:17-alpine",
                    ],
                    env,
                )
                mapping = run(["podman", "port", container, "5432/tcp"], env)
                address, separator, port = mapping.rpartition(":")
                if not separator or address != "127.0.0.1" or not port.isdigit():
                    raise LiveCheckError(
                        "Disposable database must publish only on loopback"
                    )
                wait_ready(
                    lambda: bool(
                        run(
                            [
                                "podman",
                                "exec",
                                container,
                                "pg_isready",
                                "-h",
                                "127.0.0.1",
                                "-U",
                                "postgres",
                                "-d",
                                "ku_live",
                            ],
                            env,
                        )
                    ),
                    "Disposable database",
                )
                database_url = (
                    f"postgres://postgres@127.0.0.1:{port}/ku_live?sslmode=disable"
                )
                run(
                    [str(binary), "migrate", "up"],
                    {**env, "DATABASE_URL": database_url},
                    cwd=be_dir,
                )
                api_port = free_port()
                backend_url = f"http://127.0.0.1:{api_port}"
                backend = subprocess.Popen(
                    [str(binary)],
                    cwd=be_dir,
                    stdout=backend_log,
                    stderr=backend_log,
                    env={
                        **env,
                        "PORT": str(api_port),
                        "BIND_ADDRESS": "127.0.0.1",
                        "DATABASE_URL": database_url,
                        "REDIS_URL": "redis://127.0.0.1:1",
                        "JWT_SECRET": secrets.token_hex(32),
                        "FRONTEND_URL": backend_url,
                        "ENVIRONMENT": "development",
                        "STORAGE_TYPE": "local",
                        "STORAGE_LOCAL_DIR": str(temp_dir / "uploads"),
                        "DEV_MACHINES_ENABLED": "false",
                        **extra_env,
                    },
                )
                api = urllib.request.build_opener(
                    NoRedirect(),
                    urllib.request.ProxyHandler({}),
                    urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
                )
                wait_ready(
                    lambda: request_json(api, backend_url, "/ready", timeout=2),
                    "Disposable backend",
                    process=backend,
                )
                verify_loopback_listener(api_port, env)
                yield LiveFixture(api, backend_url, container, env, server_sha256)
            finally:
                if backend is not None and backend.poll() is None:
                    backend.terminate()
                    try:
                        backend.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        backend.kill()
                        backend.wait(timeout=5)
                if container_attempted:
                    exists = subprocess.run(
                        ["podman", "container", "exists", container],
                        env=env,
                        capture_output=True,
                        timeout=30,
                        check=False,
                    )
                    if exists.returncode == 0:
                        cleanup = subprocess.run(
                            ["podman", "rm", "--force", container],
                            env=env,
                            capture_output=True,
                            timeout=30,
                            check=False,
                        )
                        if cleanup.returncode != 0:
                            raise LiveCheckError(
                                "Disposable database cleanup failed; inspect only this test container: "
                                + container
                            )
                    elif exists.returncode != 1:
                        raise LiveCheckError(
                            "Disposable database cleanup failed; inspect only this test container: "
                            + container
                        )
