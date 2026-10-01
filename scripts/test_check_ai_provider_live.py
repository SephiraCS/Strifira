"""Offline regressions for the live AI checker's explicit operator policy."""

import argparse
import contextlib
import importlib.util
import io
import json
import os
import pathlib
import types
import unittest
from unittest import mock

from ku_live_fixture import tool_environment


spec = importlib.util.spec_from_file_location(
    "check_ai_provider_live",
    pathlib.Path(__file__).with_name("check-ai-provider-live.py"),
)
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class ProviderOriginTests(unittest.TestCase):
    def test_exact_origins_match_backend_normalization(self):
        cases = {
            "http://cliproxyapi.incus:8317/v1": "http://cliproxyapi.incus:8317",
            "http://10.11.99.1:8317/v1": "http://10.11.99.1:8317",
            "https://AI.Internal.Example./v1": "https://ai.internal.example:443",
            "https://ai.internal.example:00443/v1": "https://ai.internal.example:443",
            "http://127.0.0.1/v1": "http://127.0.0.1:80",
            "http://[2001:0db8::1]:8317/v1": "http://[2001:db8::1]:8317",
            "http://[::ffff:192.168.1.2]/v1": "http://192.168.1.2:80",
        }
        for value, origin in cases.items():
            with self.subTest(value=value):
                self.assertEqual(checker.provider_origin(value), origin)
                self.assertEqual(checker.provider_url(value), value)

    def test_invalid_or_ambiguous_urls_never_become_trust_entries(self):
        cases = [
            "ftp://ai.example/v1",
            "HTTP://ai.example/v1",
            "http://user:secret@ai.example/v1",
            "http://ai.example/v1?secret=x",
            "http://ai.example/v1?",
            "http://ai.example/v1#",
            "http://*.example/v1",
            "http://ai_example/v1",
            "http://-ai.example/v1",
            "http://ai-.example/v1",
            "http://ai..example/v1",
            "http://a" + "a" * 63 + ".example/v1",
            "http://münchen.example/v1",
            "http://K.example/v1",
            "http://ai.example:/v1",
            "http://ai.example:0/v1",
            "http://ai.example:65536/v1",
            "http://ai.example:-1/v1",
            "http://ai.example:１２/v1",
            "http://ai.example:" + "9" * 5000 + "/v1",
            "http://[fe80::1%25eth0]:8317/v1",
            "http://[::1]trailing/v1",
            "http://::1/v1",
            "http://ai.example\\other/v1",
            "http://ai.example/ v1",
            "http://ai.\nexample/v1",
            "http://ai.example/\x00v1",
            "http://ai.example/%broken",
        ]
        for value in cases:
            with self.subTest(value=value):
                with self.assertRaises(argparse.ArgumentTypeError):
                    checker.provider_url(value)

    def test_dry_run_reports_exact_approval_without_reading_keys_or_starting(self):
        arguments = [
            "check-ai-provider-live.py",
            "--provider-env-file",
            "/does/not/exist",
            "--provider-base-url",
            "http://cliproxyapi.incus:8317/v1",
            "--model",
            "owner-selected-model",
            "--trust-provider-origin",
            "--dry-run",
        ]
        output = io.StringIO()
        with (
            mock.patch("sys.argv", arguments),
            mock.patch.object(checker, "provider_key") as key,
            mock.patch.object(checker, "disposable_backend") as backend,
            contextlib.redirect_stdout(output),
        ):
            self.assertEqual(checker.main(), 0)
        key.assert_not_called()
        backend.assert_not_called()
        policy = json.loads(output.getvalue().splitlines()[-1])[
            "ai_provider_network_policy"
        ]
        self.assertEqual(policy["trusted_origins"], ["http://cliproxyapi.incus:8317"])
        self.assertTrue(policy["trust_provider_origin"])


class FixturePolicyTests(unittest.TestCase):
    def test_policy_and_actual_binary_hash_are_bound_to_success_receipt(self):
        for explicit_trust in (False, True):
            with self.subTest(explicit_trust=explicit_trust):
                self.check_acceptance(explicit_trust)

    def check_acceptance(self, explicit_trust):
        args = argparse.Namespace(
            provider_env_file=pathlib.Path("/unused-owner-projection"),
            provider_base_url="http://cliproxyapi.incus:8317/v1",
            model="owner-selected-model",
            server_bin=pathlib.Path("/reviewed/backend"),
            trust_provider_origin=explicit_trust,
        )
        observed = {}
        saved = {"has_api_key": False, "description": None}
        backend_url = "http://127.0.0.1:12345"
        actual_binary_hash = "a" * 64

        @contextlib.contextmanager
        def backend(server_bin, be_dir, *, extra_env, label):
            observed.update(extra_env)
            self.assertEqual(server_bin, args.server_bin)
            self.assertEqual(label, "ai-provider-live")
            yield types.SimpleNamespace(
                api=object(),
                base_url=backend_url,
                server_sha256=actual_binary_hash,
            )

        def request(_opener, base_url, path, *, method="GET", data=None, **_kwargs):
            if base_url == args.provider_base_url and path == "/models":
                return {"data": [{"id": args.model}]}
            self.assertEqual(base_url, backend_url)
            if path.endswith("/ai-settings") and method == "PATCH":
                saved["has_api_key"] = data.get("api_key") is not None
                return {"has_api_key": saved["has_api_key"]}
            if path.endswith("/ai-settings/test"):
                if saved["has_api_key"]:
                    return {"model": args.model, "latency_ms": 3}
                return {"error": {"code": "AI_NOT_CONFIGURED"}}
            if path.endswith("/teams"):
                return {"id": "fixture-team"}
            if path.endswith("/issues"):
                return {"identifier": "AITEST-1"}
            if path.endswith("/expand-description"):
                return {"description": "<p>Usable saved issue description.</p>"}
            if path.endswith("/AITEST-1"):
                if method == "PATCH":
                    saved["description"] = data["description"]
                    return {}
                return {"description": saved["description"]}
            if path.endswith("/ai/rework"):
                return {"text": "<p>Usable rewritten comment.</p>"}
            if path == "/api/auth/register" or path == "/api/workspaces":
                return {}
            if path.startswith("/api/workspaces/") and method == "DELETE":
                return None
            self.fail("Unexpected offline fixture request")

        with (
            mock.patch.dict(
                os.environ,
                {
                    "AI_PROVIDER_TRUSTED_ORIGINS": "http://unrequested.internal:8888",
                    "DATABASE_URL": "postgres://must-not-inherit/example",
                },
            ),
            mock.patch.object(
                checker, "provider_key", return_value="fixture-only-key"
            ),
            mock.patch.object(checker, "request_json", side_effect=request),
            mock.patch.object(checker, "disposable_backend", side_effect=backend),
            contextlib.redirect_stderr(io.StringIO()),
        ):
            self.assertNotIn("AI_PROVIDER_TRUSTED_ORIGINS", tool_environment())
            self.assertNotIn("DATABASE_URL", tool_environment())
            receipt = checker.acceptance(args)
        expected = "http://cliproxyapi.incus:8317" if explicit_trust else ""
        self.assertEqual(observed, {"AI_PROVIDER_TRUSTED_ORIGINS": expected})
        self.assertEqual(receipt["backend_sha256"], actual_binary_hash)
        self.assertEqual(
            receipt["ai_provider_network_policy"],
            {
                "default": "public_only",
                "trusted_origins": [expected] if explicit_trust else [],
                "trust_provider_origin": explicit_trust,
            },
        )
        self.assertNotIn("fixture-only-key", json.dumps(receipt))


if __name__ == "__main__":
    unittest.main()
