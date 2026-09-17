-- Rollback intentionally destroys all API keys; MCP clients must be re-authorized.
DROP TABLE IF EXISTS api_keys;
