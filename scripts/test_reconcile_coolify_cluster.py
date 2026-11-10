"""Recovery-plan regressions using a stateful Coolify API fixture."""

import copy
import io
import json
import unittest
import urllib.error
from unittest.mock import patch

import reconcile_coolify_cluster as cluster


class CoolifyFixture:
    def __init__(self):
        self.apps = {}
        self.envs = {}
        self.writes = []
        self.storages = {}
        self.deployments = []
        for index in range(4):
            uuid = f"node{index}"
            self.apps[uuid] = {
                "uuid": uuid, "name": uuid, "fqdn": cluster.APPLICATION if index == 0 else None,
                "git_repository": cluster.REPOSITORY + (".git" if index else ""),
                "git_branch": "main", "git_commit_sha": "old-revision", "environment_id": 1,
                "destination": {"uuid": "destination1", "network": "coolify"},
                "build_pack": "dockerfile", "custom_network_aliases": "existing-alias" if index == 0 else None,
                "settings": {"include_source_commit_in_build": False,
                             "is_consistent_container_name_enabled": False},
                "health_check_path": "/health", "health_check_enabled": False,
                "custom_labels": "keep these labels", "unrelated_secret": "do-not-emit",
            }
            self.envs[uuid] = [
                {"key": "NATS_SEED_URLS", "value": "nats://node1:6222", "is_preview": False,
                 "is_runtime": True, "is_buildtime": True, "comment": "keep this comment"},
                {"key": "NATS_SEED_URLS", "value": "nats://preview-peer:6222", "is_preview": True},
                {"key": "DATABASE_URL", "value": "do-not-emit", "is_preview": False},
            ]
            self.storages[uuid] = {"persistent_storages": [], "file_storages": []}

    def request(self, path, method="GET", data=None):
        if method == "GET":
            if path == "deployments":
                return copy.deepcopy(self.deployments)
            if path == "applications":
                return copy.deepcopy(list(self.apps.values()))
            parts = path.split("/")
            if path.endswith("/storages"):
                return copy.deepcopy(self.storages[parts[1]])
            return copy.deepcopy(self.envs[parts[1]] if len(parts) == 3 else self.apps[parts[1]])
        self.writes.append((path, method, data))
        uuid = path.split("/")[1]
        if path.endswith("/storages"):
            self.storages[uuid]["persistent_storages"].append({
                **data, "name": uuid + "-" + data["name"], "host_path": None,
            })
        elif path.endswith("/envs"):
            entry = next(env for env in self.envs[uuid]
                         if env["key"] == data["key"] and not env["is_preview"])
            entry.update(data)
        else:
            data = copy.deepcopy(data)
            for key in cluster.SETTING_FIELDS:
                if key in data:
                    self.apps[uuid]["settings"][key] = data.pop(key)
            if "custom_network_aliases" in data:
                # Coolify's setter stores JSON; the API getter normalizes to CSV.
                data["custom_network_aliases"] = ",".join(cluster.aliases(data))
            self.apps[uuid].update(data)
        return {}


class RecoveryPlanTests(unittest.TestCase):
    def test_plan_discovers_nodes_and_excludes_other_environments_without_mutation(self):
        api = CoolifyFixture()
        other = copy.deepcopy(api.apps["node0"])
        other.update(uuid="staging", environment_id=2, fqdn=None)
        api.apps["staging"] = other
        plan = cluster.plan(api)
        self.assertEqual(len(plan["nodes"]), 4)
        self.assertEqual(plan["nodes"][0]["seeds_after"], "nats://node1:6222,nats://node2:6222,nats://node3:6222")
        self.assertEqual(plan["nodes"][0]["after"]["custom_network_aliases"], "existing-alias,node0")
        self.assertNotIn("do-not-emit", json.dumps(plan))
        self.assertEqual(api.writes, [])

    def test_apply_preserves_secrets_preview_envs_aliases_and_unrelated_fields(self):
        api = CoolifyFixture()
        before = copy.deepcopy(api.envs)
        cluster.apply(api, cluster.plan(api))
        for uuid, app in api.apps.items():
            self.assertEqual(app["custom_labels"], "keep these labels")
            self.assertEqual(app["unrelated_secret"], "do-not-emit")
            self.assertEqual(app["health_check_path"], "/ready")
            self.assertTrue(app["health_check_enabled"])
            self.assertEqual(app["git_commit_sha"], "HEAD")
            self.assertTrue(app["settings"]["is_consistent_container_name_enabled"])
            self.assertIn(uuid, cluster.aliases(app))
            self.assertEqual(api.envs[uuid][1:], before[uuid][1:])
            self.assertEqual(api.envs[uuid][0]["comment"], "keep this comment")
            self.assertTrue(api.envs[uuid][0]["is_buildtime"])
        self.assertTrue(all(method == "PATCH" or path.endswith("/storages")
                            for path, method, _ in api.writes))
        count = len(api.writes)
        cluster.apply(api, cluster.plan(api))
        self.assertEqual(len(api.writes), count, "a second application must be a no-op")

    def test_aliases_accept_coolify_csv_and_legacy_json_but_reject_invalid_names(self):
        for raw in ["peer-a,peer-b", '["peer-a", "peer-b"]']:
            self.assertEqual(cluster.aliases({"custom_network_aliases": raw}), ["peer-a", "peer-b"])
        for raw in ["peer-a,,peer-b", "peer-a/peer-b", '["peer-a", false]', "[broken", []]:
            with self.assertRaises(cluster.ClusterError):
                cluster.aliases({"custom_network_aliases": raw})

    def test_active_deploy_or_changed_plan_cannot_mutate(self):
        api = CoolifyFixture()
        approved = cluster.plan(api)
        api.deployments = [{"status": "in_progress"}]
        with self.assertRaisesRegex(cluster.ClusterError, "active deployments"):
            cluster.apply(api, approved)
        api.deployments = []
        api.apps["node1"]["custom_network_aliases"] = '["concurrent-alias"]'
        with self.assertRaisesRegex(cluster.ClusterError, "changed since"):
            cluster.apply(api, approved)
        self.assertEqual(api.writes, [])

    def test_concurrent_change_after_first_patch_stops_before_overwriting_next_node(self):
        api = CoolifyFixture()
        original = api.request

        def request(path, method="GET", data=None):
            result = original(path, method, data)
            if path == "applications/node0/envs" and method == "PATCH":
                api.apps["node1"]["custom_network_aliases"] = '["concurrent-alias"]'
            return result

        api.request = request
        with self.assertRaisesRegex(cluster.ClusterError, "Concurrent node"):
            cluster.apply(api, cluster.plan(api))
        self.assertEqual(api.apps["node1"]["custom_network_aliases"], '["concurrent-alias"]')
        self.assertTrue(all("node0" in path for path, _, _ in api.writes))

    def test_seed_credentials_and_alias_collision_are_rejected(self):
        api = CoolifyFixture()
        api.envs["node0"][0]["value"] = "nats://user:do-not-emit@peer:6222"
        with self.assertRaises(cluster.ClusterError) as failure:
            cluster.plan(api)
        self.assertNotIn("do-not-emit", str(failure.exception))
        api = CoolifyFixture()
        api.apps["node0"]["custom_network_aliases"] = '["node1"]'
        with self.assertRaisesRegex(cluster.ClusterError, "another node"):
            cluster.plan(api)
        self.assertEqual(api.writes, [])

    def test_nonpersistent_api_update_is_detected(self):
        api = CoolifyFixture()
        original = api.request
        api.request = lambda path, method="GET", data=None: (
            original(path, method, data) if method == "GET" else {}
        )
        with self.assertRaisesRegex(cluster.ClusterError, "did not retain"):
            cluster.apply(api, cluster.plan(api))

    def test_shared_jetstream_volume_is_rejected(self):
        api = CoolifyFixture()
        for uuid in ["node0", "node1"]:
            api.storages[uuid]["persistent_storages"] = [
                {"name": "shared", "mount_path": "/app/uploads", "host_path": None},
            ]
        with self.assertRaisesRegex(cluster.ClusterError, "must not share"):
            cluster.plan(api)
        self.assertEqual(api.writes, [])

    def test_http_error_never_emits_api_token_or_response_body(self):
        with patch.dict("os.environ", {"COOLIFY_API_KEY": "do-not-emit-token"}):
            api = cluster.Coolify()
        failure = urllib.error.HTTPError(cluster.COOLIFY, 401, "do-not-emit-response", {}, io.BytesIO())
        with patch.object(api.opener, "open", side_effect=failure):
            with self.assertRaisesRegex(cluster.ClusterError, "HTTP 401") as caught:
                api.request("applications")
        self.assertNotIn("do-not-emit", str(caught.exception))
        self.assertIsNone(cluster.NoRedirect().redirect_request(None, None, 302, "", {}, "https://other.example"))


if __name__ == "__main__":
    unittest.main()
