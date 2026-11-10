-- 2026-11-10: where a request writing the log came from, as its client declares
-- it: 'assistant' for the AI assistant, through the MCP of the collector and its
-- agent, 'mcp' for another client of that MCP; NULL for the web interface, the
-- API and the collector itself. log_request_id is the X-Request-ID the client
-- gave, which the agent and the MCP write on their audit lines: it ties the
-- entry to the question asked. Both are declared by the client, not proven:
-- they tell how an action was made, never who made it, which log_user says.
ALTER TABLE log
  ADD COLUMN IF NOT EXISTS log_origin VARCHAR(16) NULL DEFAULT NULL
    COMMENT 'client the request came through, as declared: assistant, mcp'
    AFTER log_impersonator,
  ADD COLUMN IF NOT EXISTS log_request_id VARCHAR(64) NULL DEFAULT NULL
    COMMENT 'X-Request-ID of the request, as declared by its client'
    AFTER log_origin;
