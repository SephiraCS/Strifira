#!/usr/bin/env python3
"""Opt-in real development box acceptance in a dedicated development VM.

Uses a reviewed Strifira backend and worker binary, a disposable PostgreSQL/backend/
account/workspace, and an installed authenticated coding CLI. All Git work stays
in a tiny local fixture repository. No deployed service, application .env,
personal API key, Forgejo credentials, push, or pull request is used.

Run after other acceptance/build jobs release the VM's bounded workload lane.
"""

import argparse
import contextlib
import hashlib
import json
import os
import pathlib
import secrets
import signal
import subprocess
import sys
import tempfile
import time
import uuid

from strifira_live_fixture import (
    LiveCheckError,
    disposable_backend,
    request_json,
    run,
    tool_environment,
)


def require(condition, description):
    if not condition:
        raise LiveCheckError(description)


def wait_for(check, label, timeout=180):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = check()
        if value:
            return value
        time.sleep(0.3)
    raise LiveCheckError("Timed out waiting for " + label)


def stop_process(process):
    if process is None or process.poll() is not None:
        return
    with contextlib.suppress(ProcessLookupError):
        os.killpg(process.pid, signal.SIGTERM)
    try:
        process.wait(timeout=12)
    except subprocess.TimeoutExpired:
        with contextlib.suppress(ProcessLookupError):
            os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=5)


def process_terminated(pid):
    try:
        text = pathlib.Path(f"/proc/{pid}/stat").read_text()
    except FileNotFoundError:
        return True
    return ") Z " in text


def acceptance(args):
    strifira_dir = pathlib.Path(__file__).resolve().parent.parent
    be_dir = strifira_dir / "BE"
    env = tool_environment()
    worker_bin = args.worker_bin.resolve()
    provider_path = args.provider_path.resolve()
    require(
        worker_bin.is_file() and os.access(worker_bin, os.X_OK),
        "Reviewed worker binary is missing or not executable",
    )
    require(
        provider_path.is_file() and os.access(provider_path, os.X_OK),
        "Configured provider executable is missing",
    )
    report = {
        "provider": args.provider,
        "isolation": "disposable_database_backend_workspace_repository",
        "publishing": "disabled",
        "worker_sha256": hashlib.sha256(worker_bin.read_bytes()).hexdigest(),
    }
    with tempfile.TemporaryDirectory(prefix="strifira-dev-box-live-") as temporary:
        root = pathlib.Path(temporary)
        repository = root / "repository"
        repository.mkdir()
        work_root = root / "worktrees"
        work_root.mkdir(mode=0o700)
        full_name = "fixture/acceptance-" + uuid.uuid4().hex[:12]
        repository_url = "https://forgejo.fixture.invalid/" + full_name
        for argv in (
            ["init", "-b", "main"],
            ["config", "user.name", "Disposable Strifira acceptance"],
            ["config", "user.email", "strifira@example.invalid"],
            ["remote", "add", "origin", repository_url],
        ):
            run(["git", *argv], env, cwd=repository)
        (repository / ".gitignore").write_text("__pycache__/\n")
        (repository / "calculator.py").write_text("def add(a, b):\n    return a - b\n")
        (repository / "test_calculator.py").write_text(
            "import unittest\nfrom calculator import add\n\nclass AdditionTest(unittest.TestCase):\n    def test_add(self):\n        self.assertEqual(add(2, 3), 5)\n\nif __name__ == '__main__':\n    unittest.main()\n"
        )
        run(["git", "add", "."], env, cwd=repository)
        run(
            ["git", "commit", "-m", "Disposable acceptance baseline"],
            env,
            cwd=repository,
        )
        original_head = run(["git", "rev-parse", "HEAD"], env, cwd=repository)
        original_index = (repository / ".git/index").read_bytes()

        with disposable_backend(
            args.server_bin, be_dir, label="dev-box-live"
        ) as fixture:
            api, base_url = fixture.api, fixture.base_url
            report["server_sha256"] = fixture.server_sha256
            nonce = uuid.uuid4().hex[:12]
            request_json(
                api,
                base_url,
                "/api/auth/register",
                method="POST",
                expected=201,
                data={
                    "name": "Disposable development box acceptance",
                    "email": f"strifira-dev-box-{nonce}@example.invalid",
                    "password": secrets.token_hex(24) + "Aa9!",
                },
            )
            slug = "dev-box-live-" + nonce
            workspace = request_json(
                api,
                base_url,
                "/api/workspaces",
                method="POST",
                expected=201,
                data={"name": "Disposable development box acceptance", "slug": slug},
            )
            prefix = "/api/workspaces/" + slug
            team = request_json(
                api,
                base_url,
                prefix + "/teams",
                method="POST",
                expected=201,
                data={"name": "Development box acceptance", "key": "BOXTEST"},
            )
            issue = request_json(
                api,
                base_url,
                prefix + "/issues",
                method="POST",
                expected=201,
                data={
                    "team_id": team["id"],
                    "title": "Fix fixture addition",
                    "description": "<p>Update calculator.py so add(2,3) returns 5. Keep the existing test unchanged. No publishing.</p>",
                },
            )
            repository_id = str(uuid.uuid4())
            # Only this disposable database receives fake linked repository metadata.
            # No external Forgejo call or server/global credential is involved.
            fixture.sql(
                f"INSERT INTO forgejo_repos (id,workspace_id,forgejo_repo_id,full_name,instance_url,default_branch,webhook_secret,is_active) VALUES ('{repository_id}','{workspace['id']}',1,'{full_name}','https://forgejo.fixture.invalid','main','fixture-placeholder',true);"
            )
            request_json(
                api,
                base_url,
                prefix + "/dev-machine-scope-setting",
                method="PUT",
                data={
                    "scope_type": "workspace",
                    "forgejo_repo_id": repository_id,
                    "base_branch": "main",
                },
            )
            registration = request_json(
                api,
                base_url,
                prefix + "/dev-boxes",
                method="POST",
                expected=201,
                data={"name": "Disposable real VM worker"},
            )
            box_id, worker_token = registration["box"]["id"], registration["token"]
            require(
                worker_token.startswith("strifira_") and len(worker_token) == 68,
                "Registration did not return a dedicated worker token",
            )
            token_file = root / "worker.token"
            token_file.touch(mode=0o600)
            token_file.write_text(worker_token + "\n")
            config_file = root / "worker.json"
            config_file.write_text(
                json.dumps(
                    {
                        "server_url": base_url,
                        "localhost_http": True,
                        "token_file": str(token_file),
                        "work_root": str(work_root),
                        "execution_mode": "trusted-local",
                        "repositories": {
                            full_name: {"path": str(repository), "url": repository_url}
                        },
                        "providers": {args.provider: str(provider_path)},
                    }
                )
            )
            config_file.chmod(0o600)
            run(
                [str(worker_bin), "--config", str(config_file), "--once"],
                env,
                timeout=30,
            )
            boxes = request_json(api, base_url, prefix + "/dev-boxes")
            require(
                len(boxes) == 1 and boxes[0]["online"],
                "Worker heartbeat did not mark the registered box online",
            )
            report["providers"] = boxes[0]["providers"]

            def create_job(test_command, description):
                return request_json(
                    api,
                    base_url,
                    prefix + f"/dev-boxes/{box_id}/jobs",
                    method="POST",
                    expected=202,
                    data={
                        "issue_id": issue["id"],
                        "provider": args.provider,
                        "prompt": "Fix only calculator.py to use addition. Do not edit tests or commit/push/publish changes. "
                        + description,
                        "test_command": test_command,
                        "acceptance_criteria": [
                            "add(2,3) returns 5",
                            "Existing tests remain unchanged",
                            "No publishing",
                        ],
                        "max_runtime_seconds": args.timeout,
                    },
                )

            def get_job(job):
                return request_json(
                    api, base_url, prefix + "/dev-box-jobs/" + job["id"]
                )

            def terminal(job):
                current = get_job(job)
                return (
                    current
                    if current["status"]
                    in {"succeeded", "failed", "cancelled", "timeout"}
                    else None
                )

            def execute_once(job, cancel_marker=None):
                process = None
                with (root / "worker.log").open("wb") as log:
                    try:
                        process = subprocess.Popen(
                            [str(worker_bin), "--config", str(config_file), "--once"],
                            env=env,
                            stdout=log,
                            stderr=log,
                            start_new_session=True,
                        )
                        if cancel_marker is not None:

                            def test_started():
                                require(
                                    process.poll() is None,
                                    "Worker stopped before independent cancellation test started",
                                )
                                return (
                                    cancel_marker.is_file()
                                    and cancel_marker.stat().st_size > 0
                                )

                            wait_for(
                                test_started,
                                "real independent test process",
                                timeout=args.timeout,
                            )
                            require(
                                get_job(job)["status"] == "running",
                                "Cancellation fixture was not running",
                            )
                            request_json(
                                api,
                                base_url,
                                prefix + "/dev-box-jobs/" + job["id"] + "/cancel",
                                method="POST",
                                data={},
                            )
                        finished = wait_for(
                            lambda: terminal(job),
                            "job completion",
                            timeout=args.timeout + 20,
                        )
                        process.wait(timeout=15)
                        require(
                            process.returncode == 0,
                            "Worker failed to complete its leased job",
                        )
                        return finished
                    finally:
                        stop_process(process)

            success = execute_once(
                create_job(
                    [
                        sys.executable,
                        "-c",
                        "from calculator import add;assert add(2,3)==5;print('independent addition assertion passed')",
                    ],
                    "The worker will independently run the test suite after you finish.",
                )
            )
            result = success["result"]
            require(
                success["status"] == "succeeded"
                and result["test_status"] == "passed"
                and result["test_exit_code"] == 0,
                "Real provider and independent test job did not succeed",
            )
            require(
                result["branch"] == success["working_branch"],
                "Worker did not preserve its assigned job branch",
            )
            require(
                result["changed_files"] == ["calculator.py"]
                and "+    return a + b" in result["patch"],
                "Successful job did not return the expected review patch",
            )
            logs = request_json(
                api,
                base_url,
                prefix + "/dev-box-jobs/" + success["id"] + "/logs?limit=500",
            )
            require(
                any(item["stream"] == "stdout" for item in logs)
                and any(
                    "Running configured tests independently" in item["content"]
                    for item in logs
                ),
                "Real provider/test logs were not streamed through the API",
            )
            require(
                worker_token not in json.dumps(result)
                and worker_token not in json.dumps(logs),
                "Worker token leaked into published result or logs",
            )
            report["success"] = {
                "status": success["status"],
                "test_status": result["test_status"],
                "changed_files": result["changed_files"],
                "patch_sha256": hashlib.sha256(result["patch"].encode()).hexdigest(),
                "log_entries": len(logs),
            }

            failure = execute_once(
                create_job(
                    [
                        sys.executable,
                        "-c",
                        "print('intentional independent test failure');raise SystemExit(23)",
                    ],
                    "The intentionally failing independent test is an acceptance fixture. Finish the coding fix normally; the worker must report its separate failure.",
                )
            )
            failed = failure["result"]
            require(
                failure["status"] == "failed"
                and failed["test_status"] == "failed"
                and failed["test_exit_code"] == 23,
                "Independent failing tests did not override the coding result",
            )
            require(failed["patch"], "Failed tests discarded the reviewable patch")
            report["failed_test"] = {
                "status": failure["status"],
                "test_status": failed["test_status"],
                "test_exit_code": failed["test_exit_code"],
                "provider_exit_code": failed["exit_code"],
            }

            cancel_marker = root / "test-processes.json"
            cancellation_code = (
                "import json,os,pathlib,subprocess,time;child=subprocess.Popen(['sleep','120']);pathlib.Path("
                + repr(str(cancel_marker))
                + ").write_text(json.dumps([os.getpid(),child.pid]));time.sleep(120)"
            )
            cancelled = execute_once(
                create_job(
                    [sys.executable, "-c", cancellation_code],
                    "Finish the fix normally. Do not run the cancellation fixture yourself; only the worker executes that independent command.",
                ),
                cancel_marker,
            )
            require(
                cancelled["status"] == "cancelled",
                "Running job cancellation was not recorded",
            )
            pids = json.loads(cancel_marker.read_text())
            wait_for(
                lambda: all(process_terminated(pid) for pid in pids),
                "test process group termination",
                timeout=5,
            )
            report["cancellation"] = {
                "status": cancelled["status"],
                "process_group_terminated": True,
            }

            require(
                run(["git", "rev-parse", "HEAD"], env, cwd=repository) == original_head,
                "Original checkout HEAD changed",
            )
            require(
                (repository / ".git/index").read_bytes() == original_index,
                "Original checkout index changed",
            )
            require(
                (repository / "calculator.py").read_text()
                == "def add(a, b):\n    return a - b\n",
                "Original checkout working tree changed",
            )
            report["original_checkout_preserved"] = True
            request_json(
                api,
                base_url,
                prefix + f"/dev-boxes/{box_id}",
                method="DELETE",
                expected=204,
            )
            token_file.unlink()
    report["fixture_cleanup"] = "completed"
    return report


def main():
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument(
        "--server-bin",
        type=pathlib.Path,
        required=True,
        help="Reviewed Strifira backend binary",
    )
    parser.add_argument(
        "--worker-bin",
        type=pathlib.Path,
        required=True,
        help="Reviewed dev-box-worker binary",
    )
    parser.add_argument(
        "--provider", choices=("claude-code", "codex", "opencode"), required=True
    )
    parser.add_argument(
        "--provider-path",
        type=pathlib.Path,
        required=True,
        help="Installed authenticated local CLI or owner-controlled wrapper",
    )
    parser.add_argument(
        "--timeout", type=int, default=240, help="Maximum seconds per job (30..86400)"
    )
    parser.add_argument(
        "--report", type=pathlib.Path, help="Optional secret-free evidence JSON path"
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Describe work without reading credentials or starting processes",
    )
    args = parser.parse_args()
    if args.dry_run:
        print(
            "Would create disposable Strifira/PostgreSQL/account/workspace/repository, register a dedicated protected-file worker token, run real provider success/independent failure/cancellation jobs with no publishing, and delete the fixture."
        )
        return 0
    require(30 <= args.timeout <= 86400, "Job timeout must be 30..86400 seconds")

    def interrupted(_signum, _frame):
        raise LiveCheckError("Acceptance interrupted; fixture cleanup requested")

    signal.signal(signal.SIGTERM, interrupted)
    try:
        report = acceptance(args)
    except KeyboardInterrupt:
        print("Acceptance interrupted; fixture cleanup requested", file=sys.stderr)
        return 130
    except (
        LiveCheckError,
        OSError,
        KeyError,
        TypeError,
        ValueError,
        subprocess.TimeoutExpired,
    ) as error:
        print(
            str(error)
            if isinstance(error, LiveCheckError)
            else "Acceptance failed with an unexpected fixture error",
            file=sys.stderr,
        )
        return 1
    if args.report:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
