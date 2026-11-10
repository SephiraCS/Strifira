#!/usr/bin/env python3
"""Plan and apply Strifira's persistent Coolify DNS/route configuration.

The plan contains only networking and health-check fields. It never contains
credentials, starts deployments, restarts containers or modifies stored data.
"""

import http.client
import json
import os
from pathlib import Path
import re
import sys
import urllib.error
import urllib.parse
import urllib.request


COOLIFY = "https://coolify.sephira.app.br"
APPLICATION = "https://strifira.sephira.app.br"
REPOSITORY = "SephiraCS/Strifira"
SETTING_FIELDS = {"include_source_commit_in_build", "is_consistent_container_name_enabled"}


class ClusterError(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Coolify:
    def __init__(self):
        self.token = os.environ.get("COOLIFY_API_KEY")
        if not self.token:
            raise ClusterError("COOLIFY_API_KEY is required")
        self.opener = urllib.request.build_opener(NoRedirect())

    def request(self, path, method="GET", data=None):
        request = urllib.request.Request(
            COOLIFY + "/api/v1/" + path,
            data=None if data is None else json.dumps(data).encode(),
            method=method,
            headers={
                "Authorization": "Bearer " + self.token,
                "Accept": "application/json",
                "Content-Type": "application/json",
            },
        )
        try:
            with self.opener.open(request, timeout=30) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            raise ClusterError(f"Coolify {method} failed with HTTP {error.code}") from None
        except (OSError, ValueError, http.client.HTTPException):
            # Responses and exception details can contain credentials.
            raise ClusterError(f"Coolify {method} failed; response omitted") from None


def aliases(application):
    raw = application.get("custom_network_aliases")
    if raw is not None and not isinstance(raw, str):
        raise ClusterError("Invalid existing network aliases; inspect before changing")
    try:
        # Coolify accepts JSON on write, but its model getter returns CSV.
        value = (json.loads(raw) if raw.lstrip().startswith("[")
                 else [item.strip() for item in raw.split(",")]) if raw else []
    except (TypeError, ValueError):
        raise ClusterError("Invalid existing network aliases; inspect before changing") from None
    if not isinstance(value, list) or any(
        not isinstance(alias, str) or not re.fullmatch(r"[a-zA-Z0-9_.-]+", alias) for alias in value
    ):
        raise ClusterError("Existing network aliases must contain valid names")
    return value


def repository(application):
    return application.get("git_repository", "").removesuffix(".git")


def assert_idle(client):
    # The shared host may be deploying an unrelated application. Refuse all
    # concurrent deployments rather than guessing whether a restart is safe.
    if client.request("deployments"):
        raise ClusterError("Coolify has active deployments; wait and regenerate the plan")


def plan(client):
    assert_idle(client)
    applications = client.request("applications")
    roots = [
        app for app in applications
        if APPLICATION in (app.get("fqdn") or "").split(",")
        and repository(app) == REPOSITORY and app.get("git_branch") == "main"
    ]
    if len(roots) != 1:
        raise ClusterError("Expected exactly one production Strifira application")
    root = client.request("applications/" + roots[0]["uuid"])
    destination = root["destination"]["uuid"]
    candidates = [
        app for app in applications
        if repository(app) == REPOSITORY and app.get("git_branch") == "main"
        and app.get("environment_id") == root["environment_id"]
    ]
    nodes = [client.request("applications/" + app["uuid"]) for app in candidates]
    nodes = sorted([app for app in nodes if app["destination"]["uuid"] == destination],
                   key=lambda app: app["uuid"])
    if len(nodes) < 2 or any(app["build_pack"] != "dockerfile" for app in nodes):
        raise ClusterError("Expected multiple Dockerfile nodes on one Coolify destination")
    if any(app["destination"]["network"] != root["destination"]["network"] for app in nodes):
        raise ClusterError("Nodes do not share a Docker network")
    node_ids = {app["uuid"] for app in nodes}
    # Never steal an alias from another application, including another environment.
    for app in applications:
        if app["uuid"] not in node_ids and node_ids.intersection(aliases(app)):
            raise ClusterError("A cluster alias is already assigned to another application")

    result = {"application": APPLICATION, "destination": destination, "nodes": []}
    for app in nodes:
        uuid = app["uuid"]
        existing = aliases(app)
        if (node_ids - {uuid}).intersection(existing):
            raise ClusterError("A node owns another node's alias")
        desired = {
            "custom_network_aliases": ",".join(dict.fromkeys([*existing, uuid])),
            # Coolify field names, always set to the required state, never opt-in.
            "health_check_enabled": True,
            "health_check_type": "http",
            "health_check_path": "/ready",
            "health_check_port": "3000",
            "health_check_host": "localhost",
            "health_check_scheme": "http",
            "health_check_method": "GET",
            "health_check_return_code": 200,
            # All nodes follow the same main revision instead of frozen replicas.
            "git_commit_sha": "HEAD",
            "include_source_commit_in_build": True,
            # Coolify must stop the old NATS process before reusing its volume.
            "is_consistent_container_name_enabled": True,
        }
        before = {key: (app.get("settings", {}).get(key)
                        if key in SETTING_FIELDS else app.get(key))
                  for key in desired}
        envs = client.request("applications/" + uuid + "/envs")
        seeds = [env for env in envs
                 if env["key"] == "NATS_SEED_URLS" and not env.get("is_preview")]
        if len(seeds) != 1 or not seeds[0].get("is_runtime"):
            raise ClusterError("Expected one runtime NATS_SEED_URLS per node")
        # These URLs must contain no credentials; the public plan may include them.
        for seed in seeds[0]["value"].split(","):
            parsed = urllib.parse.urlsplit(seed.strip())
            if parsed.scheme != "nats" or not parsed.hostname or parsed.username or parsed.password:
                raise ClusterError("Seed URL is invalid or contains credentials; plan omitted")
        storages = client.request("applications/" + uuid + "/storages")
        persistent = [item for item in storages["persistent_storages"]
                      if item["mount_path"] == "/app/uploads"]
        files = [item for item in storages["file_storages"]
                 if item["mount_path"] == "/app/uploads"]
        if len(persistent) > 1 or files:
            raise ClusterError("Ambiguous uploads mounts; inspect before changing storage")
        storage = ({key: persistent[0].get(key) for key in ["name", "mount_path", "host_path"]}
                   if persistent else None)
        # Every node needs its own durable JetStream directory. Sharing a volume
        # between embedded NATS processes would corrupt their raft state.
        desired_storage = storage or {
            "name": uuid + "-strifira-uploads", "mount_path": "/app/uploads", "host_path": None,
        }
        result["nodes"].append({
            "uuid": uuid,
            "name": app["name"],
            "before": before,
            "after": desired,
            "seeds_before": seeds[0]["value"],
            "seeds_after": ",".join("nats://" + peer["uuid"] + ":6222"
                                    for peer in nodes if peer["uuid"] != uuid),
            "storage_before": storage,
            "storage_after": desired_storage,
        })
    assert_idle(client)
    volume_names = [node["storage_after"]["name"] for node in result["nodes"]]
    host_paths = [node["storage_after"]["host_path"] for node in result["nodes"]
                  if node["storage_after"]["host_path"]]
    if len(set(volume_names)) != len(volume_names) or len(set(host_paths)) != len(host_paths):
        raise ClusterError("Nodes must not share a JetStream uploads volume")
    return result


def apply(client, approved):
    current = plan(client)
    if approved != current:
        raise ClusterError("Configuration changed since this plan; regenerate and review it")
    # Mutations are deliberately separate from deployment. Updating an alias in
    # Coolify takes effect on the next rollout, not on the running container.
    for node in current["nodes"]:
        refreshed = plan(client)
        if ([item["uuid"] for item in refreshed["nodes"]] !=
                [item["uuid"] for item in current["nodes"]]
                or next(item for item in refreshed["nodes"] if item["uuid"] == node["uuid"]) != node):
            raise ClusterError("Concurrent node configuration change; regenerate the plan")
        patch = {key: value for key, value in node["after"].items()
                 if node["before"][key] != value}
        if patch:
            client.request("applications/" + node["uuid"], "PATCH", patch)
        if node["storage_before"] is None:
            client.request("applications/" + node["uuid"] + "/storages", "POST", {
                # Coolify prefixes the supplied name with the application UUID.
                "type": "persistent", "name": "strifira-uploads",
                "mount_path": "/app/uploads",
            })
        if node["seeds_before"] != node["seeds_after"]:
            client.request("applications/" + node["uuid"] + "/envs", "PATCH", {
                "key": "NATS_SEED_URLS", "value": node["seeds_after"], "is_preview": False,
            })
    remaining = plan(client)
    if any(node["before"] != node["after"] or node["seeds_before"] != node["seeds_after"]
           or node["storage_before"] != node["storage_after"]
           for node in remaining["nodes"]):
        raise ClusterError("Coolify did not retain the desired configuration; inspect before rollout")


def main():
    if len(sys.argv) == 2 and sys.argv[1] == "plan":
        print(json.dumps(plan(Coolify()), indent=2))
    elif len(sys.argv) == 3 and sys.argv[1] == "apply":
        apply(Coolify(), json.loads(Path(sys.argv[2]).read_text()))
        print("Configuration saved and verified. No deployments or restarts started.")
    else:
        raise ClusterError("Usage: reconcile_coolify_cluster.py plan | apply <reviewed-plan.json>")


if __name__ == "__main__":
    try:
        main()
    except (ClusterError, OSError, ValueError, KeyError, TypeError):
        # Do not emit a traceback or API payload, which may contain secrets.
        error = sys.exception()
        print(str(error) if isinstance(error, ClusterError) else "Invalid input or API shape", file=sys.stderr)
        print("Stopped. Changes may be partial; inspect a new plan before proceeding.", file=sys.stderr)
        sys.exit(1)
