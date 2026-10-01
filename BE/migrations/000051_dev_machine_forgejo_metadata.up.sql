-- Normalize machine links that were written with GitHub URLs after the Forgejo migration.
ALTER TABLE dev_machines ALTER COLUMN repo_provider SET DEFAULT 'forgejo';

UPDATE dev_machines AS machine
SET repo_provider = 'forgejo',
    repo_owner = split_part(repository.full_name, '/', 1),
    repo_name = split_part(repository.full_name, '/', 2),
    repo_url = rtrim(repository.instance_url, '/') || '/' || repository.full_name
FROM forgejo_repos AS repository
WHERE machine.repository_affinity_id = repository.id
  AND machine.workspace_id = repository.workspace_id;

UPDATE dev_machine_checkouts AS checkout
SET repository_full_name = repository.full_name
FROM forgejo_repos AS repository
WHERE checkout.forgejo_repo_id = repository.id
  AND checkout.workspace_id = repository.workspace_id;
