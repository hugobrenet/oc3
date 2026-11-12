-- 2026-11-10: how often the AI assistants read each resource, by backend:
-- read_count reads of its text through GET /ai/resources/{name} with a
-- backend, as the MCP servers read it for the agent, the last at last_read.
-- The reads of the admin views, which name no backend, do not count. An
-- AIManager sees them beside each resource, to tell a description the model
-- never follows. Deleted with the resource.
CREATE TABLE IF NOT EXISTS ai_resources_reads (
  resource_id INT NOT NULL,
  backend VARCHAR(8) NOT NULL,
  read_count INT NOT NULL DEFAULT 0,
  last_read DATETIME NOT NULL,
  PRIMARY KEY (resource_id, backend)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
