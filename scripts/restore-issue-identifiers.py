#!/usr/bin/env python3
"""Restore explicitly verified historical issue identifiers after a rename.

Dry-run is the default. DATABASE_URL supplies connection details through the
child environment, never argv. Only exact old=current mappings are accepted;
the tool does not infer a former team key from issue numbers.
"""

import argparse
import json
import os
import re
import subprocess
import sys
import urllib.parse

IDENTIFIER = re.compile(r"[A-Z][A-Z0-9]{0,9}-[1-9][0-9]*\Z")


class RestoreError(Exception):
    """A diagnostic without database credentials or raw database output."""


def connection_environment():
    value = os.environ.get("DATABASE_URL", "")
    try:
        parsed = urllib.parse.urlsplit(value)
        port = parsed.port or 5432
    except ValueError:
        raise RestoreError("DATABASE_URL is invalid") from None
    if (
        parsed.scheme not in {"postgres", "postgresql"}
        or not parsed.hostname
        or not parsed.path.strip("/")
        or parsed.fragment
    ):
        raise RestoreError("Set DATABASE_URL to the target PostgreSQL database")
    supported = {
        "sslmode": "PGSSLMODE",
        "sslrootcert": "PGSSLROOTCERT",
        "sslcert": "PGSSLCERT",
        "sslkey": "PGSSLKEY",
    }
    query = urllib.parse.parse_qs(parsed.query, keep_blank_values=True)
    if any(key not in supported or len(values) != 1 for key, values in query.items()):
        raise RestoreError("DATABASE_URL contains unsupported connection options")
    env = {
        key: os.environ[key]
        for key in ("PATH", "HOME", "USER", "LANG", "SSL_CERT_FILE")
        if key in os.environ
    }
    env.update(
        PGHOST=parsed.hostname,
        PGPORT=str(port),
        PGDATABASE=urllib.parse.unquote(parsed.path[1:]),
        PGUSER=urllib.parse.unquote(parsed.username or "postgres"),
        PGPASSWORD=urllib.parse.unquote(parsed.password or ""),
        PGCONNECT_TIMEOUT="10",
        PGAPPNAME="strifira-restore-issue-identifiers",
    )
    env.update({supported[key]: values[0] for key, values in query.items()})
    return env


def sql_literal(value):
    return "'" + value.replace("'", "''") + "'"


def restore(args):
    pairs = []
    seen = set()
    for mapping in args.map:
        old, separator, current = mapping.upper().partition("=")
        if (
            not separator
            or not IDENTIFIER.fullmatch(old)
            or not IDENTIFIER.fullmatch(current)
            or old == current
            or old in seen
        ):
            raise RestoreError("Use unique exact mappings such as OLD-12=NEW-12")
        seen.add(old)
        pairs.append((old, current))
    values = ",\n".join(
        "(" + sql_literal(old) + "," + sql_literal(current) + ")"
        for old, current in pairs
    )
    script = f"""
BEGIN;
SET LOCAL statement_timeout = '15s';
SET LOCAL lock_timeout = '5s';
LOCK TABLE issue_identifier_history IN SHARE ROW EXCLUSIVE MODE;
CREATE TEMP TABLE requested_identifiers (previous text PRIMARY KEY, current text);
INSERT INTO requested_identifiers VALUES {values};
CREATE TEMP TABLE verified_identifiers AS
SELECT requested.previous, issue.id AS issue_id, issue.workspace_id
FROM requested_identifiers requested
JOIN issues issue ON issue.identifier_text = requested.current
JOIN workspaces workspace ON workspace.id = issue.workspace_id
WHERE workspace.slug = {sql_literal(args.workspace)};
DO $restore$
BEGIN
    IF (SELECT count(*) FROM verified_identifiers) != {len(pairs)} THEN
        RAISE EXCEPTION 'A current identifier is missing in the requested workspace';
    END IF;
    IF EXISTS (
        SELECT 1 FROM verified_identifiers verified
        JOIN issue_identifier_history history
          ON history.workspace_id = verified.workspace_id
         AND history.identifier_text = verified.previous
        WHERE history.issue_id != verified.issue_id
    ) OR EXISTS (
        SELECT 1 FROM verified_identifiers verified
        JOIN issues issue
          ON issue.workspace_id = verified.workspace_id
         AND issue.identifier_text = verified.previous
        WHERE issue.id != verified.issue_id
    ) THEN
        RAISE EXCEPTION 'A historical identifier belongs to another issue';
    END IF;
END
$restore$;
"""
    if args.apply:
        script += """
INSERT INTO issue_identifier_history (workspace_id, identifier_text, issue_id)
SELECT workspace_id, previous, issue_id FROM verified_identifiers
ON CONFLICT (workspace_id, identifier_text) DO NOTHING;
"""
    script += (
        "SELECT json_build_object('workspace', "
        + sql_literal(args.workspace)
        + ", 'verified_mappings', count(*), 'applied', "
        + ("true" if args.apply else "false")
        + ") FROM verified_identifiers;\n"
        + ("COMMIT;\n" if args.apply else "ROLLBACK;\n")
    )
    try:
        result = subprocess.run(
            [
                "psql",
                "--no-psqlrc",
                "--quiet",
                "--tuples-only",
                "--no-align",
                "-v",
                "ON_ERROR_STOP=1",
            ],
            input=script,
            capture_output=True,
            text=True,
            env=connection_environment(),
            timeout=30,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        raise RestoreError("PostgreSQL verification did not complete") from None
    if result.returncode != 0:
        raise RestoreError(
            "Verification failed: check migration 52, workspace, exact mappings, and identity conflicts; no changes committed"
        )
    try:
        receipt = json.loads(result.stdout.strip())
    except ValueError:
        raise RestoreError("PostgreSQL returned an invalid receipt") from None
    print(json.dumps(receipt, sort_keys=True))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", required=True, help="Exact workspace slug")
    parser.add_argument(
        "--map",
        action="append",
        required=True,
        help="Verified OLD-12=NEW-12 mapping; repeat for each issue",
    )
    parser.add_argument(
        "--apply",
        action="store_true",
        help="Commit verified history records; default is dry-run",
    )
    args = parser.parse_args()
    try:
        restore(args)
    except RestoreError as error:
        print(str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
