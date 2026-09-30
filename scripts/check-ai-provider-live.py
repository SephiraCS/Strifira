#!/usr/bin/env python3
"""Verify a real provider through disposable Ku settings and editor APIs.

Creates an isolated PostgreSQL container, Ku backend, account, and workspace.
The provider key is read from an explicit owner-controlled projection and stays
in memory or the disposable database. No app .env, OAuth cache, Forgejo token,
production workspace, or deployed service is read or changed.

Requires Python 3, ss, Podman with existing registry configuration, and a reviewed
Ku backend binary (or Go to build one). Run within the development VM's bounded
workload scope, after other build/database acceptance jobs release the lane.
"""

import argparse
import contextlib
import json
import pathlib
import secrets
import shlex
import signal
import subprocess
import sys
import urllib.parse
import urllib.request
import uuid
from datetime import datetime, timezone
from html.parser import HTMLParser

from ku_live_fixture import LiveCheckError, NoRedirect, disposable_backend, request_json


def provider_key(path):
    values = []
    try:
        with path.open() as projection:
            for line in projection:
                line = line.strip()
                if line.startswith("export "):
                    line = line[7:].lstrip()
                name, separator, value = line.partition("=")
                if separator and name.strip() == "CLIPROXYAPI_API_KEY":
                    tokens = shlex.split(value, comments=True)
                    if len(tokens) != 1 or not tokens[0].strip():
                        raise LiveCheckError(
                            "Provider projection contains an invalid key entry"
                        )
                    values.append(tokens[0])
    except (OSError, ValueError) as error:
        raise LiveCheckError(
            "Cannot read the own-provider projection safely"
        ) from error
    if len(values) != 1:
        raise LiveCheckError(
            "Provider projection must contain exactly one CLIPROXYAPI_API_KEY"
        )
    return values[0]


def provider_url(value):
    parsed = urllib.parse.urlsplit(value)
    if (
        parsed.scheme not in {"http", "https"}
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or "?" in value
        or "#" in value
    ):
        raise argparse.ArgumentTypeError(
            "Provider URL must be HTTP(S) without credentials, query, or fragment"
        )
    return value.rstrip("/")


class HTMLCheck(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.nodes = 0
        self.text = []
        self.unsafe = False

    def handle_starttag(self, tag, attrs):
        self.nodes += 1
        if tag in {"script", "style", "iframe", "object", "embed"}:
            self.unsafe = True
        if any(name.lower().startswith("on") for name, _ in attrs):
            self.unsafe = True

    def handle_data(self, data):
        self.text.append(data)


def html_result(value, label):
    if not isinstance(value, str) or not value.strip() or "```" in value:
        raise LiveCheckError(label + " did not return unfenced HTML")
    parser = HTMLCheck()
    parser.feed(value)
    if parser.unsafe or parser.nodes == 0 or not "".join(parser.text).strip():
        raise LiveCheckError(label + " did not return usable, sanitized HTML")
    return {"nodes": parser.nodes, "text_chars": len("".join(parser.text))}


def acceptance(args):
    ku_dir = pathlib.Path(__file__).resolve().parent.parent
    be_dir = ku_dir / "BE"
    provider = urllib.request.build_opener(
        NoRedirect(), urllib.request.ProxyHandler({})
    )
    api_key = provider_key(args.provider_env_file.expanduser())
    models = request_json(
        provider, args.provider_base_url, "/models", authorization=api_key, timeout=20
    )
    if not any(entry.get("id") == args.model for entry in models.get("data", [])):
        raise LiveCheckError(
            "Requested model is missing from the actual provider catalog"
        )
    print(
        "Verified requested model in the own-provider catalog",
        file=sys.stderr,
        flush=True,
    )

    with disposable_backend(
        args.server_bin, be_dir, label="ai-provider-live"
    ) as fixture:
        api, backend_url = fixture.api, fixture.base_url
        workspace_path = None
        try:
            nonce = uuid.uuid4().hex[:12]
            request_json(
                api,
                backend_url,
                "/api/auth/register",
                method="POST",
                expected=201,
                data={
                    "name": "Disposable AI acceptance",
                    "email": f"ku-ai-{nonce}@example.invalid",
                    "password": secrets.token_hex(24) + "Aa9!",
                },
            )
            slug = "ai-live-" + nonce
            request_json(
                api,
                backend_url,
                "/api/workspaces",
                method="POST",
                expected=201,
                data={"name": "Disposable AI acceptance", "slug": slug},
            )
            workspace_path = "/api/workspaces/" + slug
            settings = request_json(
                api,
                backend_url,
                workspace_path + "/ai-settings",
                method="PATCH",
                data={
                    "provider": "openai_compatible",
                    "base_url": args.provider_base_url,
                    "model": args.model,
                    "api_key": api_key,
                },
            )
            if (
                not settings.get("has_api_key")
                or "api_key" in settings
                or "api_key_encrypted" in settings
                or api_key in json.dumps(settings)
            ):
                raise LiveCheckError(
                    "Saved provider settings failed the key-redaction contract"
                )
            connection = request_json(
                api,
                backend_url,
                workspace_path + "/ai-settings/test",
                method="POST",
                timeout=20,
            )
            if connection.get("model") != args.model or not isinstance(
                connection.get("latency_ms"), int
            ):
                raise LiveCheckError(
                    "Saved provider connection test returned invalid metadata"
                )
            print("Verified saved provider connection", file=sys.stderr, flush=True)
            team = request_json(
                api,
                backend_url,
                workspace_path + "/teams",
                method="POST",
                expected=201,
                data={"name": "AI acceptance", "key": "AITEST"},
            )
            issue = request_json(
                api,
                backend_url,
                workspace_path + "/issues",
                method="POST",
                expected=201,
                data={
                    "team_id": team["id"],
                    "title": "Issue editor must preserve saved descriptions after reloading",
                    "description": None,
                },
            )
            issue_path = workspace_path + "/issues/" + issue["identifier"]
            generated = request_json(
                api,
                backend_url,
                issue_path + "/expand-description",
                method="POST",
                data={},
            )["description"]
            results = {
                "connection": connection,
                "generate": html_result(
                    generated, "Title-based description generation"
                ),
            }
            request_json(
                api,
                backend_url,
                issue_path,
                method="PATCH",
                data={"description": generated},
            )
            if (
                request_json(api, backend_url, issue_path).get("description")
                != generated
            ):
                raise LiveCheckError(
                    "Generated HTML did not persist through the issue API"
                )
            results["html_persistence"] = "verified"
            print(
                "Verified title-based HTML generation and persistence",
                file=sys.stderr,
                flush=True,
            )
            rewritten = request_json(
                api,
                backend_url,
                issue_path + "/expand-description",
                method="POST",
                data={
                    "selected_text": "Reloading should keep the previously saved description."
                },
            )["description"]
            results["rewrite"] = html_result(rewritten, "Issue selection rewrite")
            reworked = request_json(
                api,
                backend_url,
                workspace_path + "/ai/rework",
                method="POST",
                data={
                    "text": "We confirmed reloading removes the description. Keep the saved text after reload.",
                    "context": "A comment on the issue-editor persistence bug.",
                },
            )["text"]
            results["rework"] = html_result(reworked, "Workspace editor rework")
            print(
                "Verified issue selection and workspace editor HTML rewrites",
                file=sys.stderr,
                flush=True,
            )
            cleared = request_json(
                api,
                backend_url,
                workspace_path + "/ai-settings",
                method="PATCH",
                data={"api_key": None},
            )
            if cleared.get("has_api_key"):
                raise LiveCheckError(
                    "Provider key removal did not clear the saved credential"
                )
            unconfigured = request_json(
                api,
                backend_url,
                workspace_path + "/ai-settings/test",
                method="POST",
                expected=400,
            )
            if unconfigured.get("error", {}).get("code") != "AI_NOT_CONFIGURED":
                raise LiveCheckError("Removed key did not disable provider requests")
            results["key_removal"] = "verified"
            print("Verified saved provider-key removal", file=sys.stderr, flush=True)
            results["isolation"] = "disposable_database_backend_workspace"
            results["backend_sha256"] = fixture.server_sha256
            return results
        finally:
            if api is not None and workspace_path is not None:
                with contextlib.suppress(Exception):
                    request_json(
                        api,
                        backend_url,
                        workspace_path + "/ai-settings",
                        method="PATCH",
                        data={"api_key": None},
                        timeout=5,
                    )
                    request_json(
                        api, backend_url, workspace_path, method="DELETE", timeout=5
                    )


def main():
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument(
        "--provider-env-file",
        type=pathlib.Path,
        required=True,
        help="Own CLIPROXYAPI_API_KEY projection; never an OAuth cache",
    )
    parser.add_argument(
        "--provider-base-url",
        type=provider_url,
        required=True,
        help="OpenAI-compatible base URL including /v1",
    )
    parser.add_argument(
        "--model", required=True, help="Actual model ID to test (no automatic fallback)"
    )
    parser.add_argument(
        "--server-bin",
        type=pathlib.Path,
        help="Reviewed Ku backend binary; otherwise builds the current tree",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Describe the isolated operations without reading the key or starting anything",
    )
    parser.add_argument(
        "--report",
        type=pathlib.Path,
        help="Write the secret-free success receipt after fixture cleanup",
    )
    args = parser.parse_args()
    if args.dry_run:
        print(
            "Would verify the provider catalog, create disposable PostgreSQL/Ku/account/workspace, save own-provider settings, test connection and HTML generation/rewrite/rework, verify key removal, then delete the fixture."
        )
        return 0

    def interrupted(_signum, _frame):
        raise LiveCheckError(
            "Acceptance interrupted; disposable fixture cleanup requested"
        )

    signal.signal(signal.SIGTERM, interrupted)
    try:
        results = acceptance(args)
    except KeyboardInterrupt:
        print(
            "Acceptance interrupted; disposable fixture cleanup requested",
            file=sys.stderr,
        )
        return 130
    except (
        LiveCheckError,
        OSError,
        KeyError,
        TypeError,
        subprocess.TimeoutExpired,
    ) as error:
        # Unknown response/process errors may contain secrets; only the
        # explicitly controlled LiveCheckError messages are printable.
        print(
            str(error)
            if isinstance(error, LiveCheckError)
            else "Acceptance failed with an unexpected fixture error",
            file=sys.stderr,
        )
        return 1
    results["verified_at"] = datetime.now(timezone.utc).isoformat()
    if args.report is not None:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(results, indent=2, sort_keys=True) + "\n")
    print(json.dumps(results, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
