-- 2026-11-13: the runs of the AI schedules on the unattended route of the agent.
-- request_id is the X-Request-ID the agent gave the run, which its audit lines
-- carry with the run ID: it finds the run in the logs of both. calls is the
-- summary the agent gave of the calls of the tools, as JSON, by identifiers only:
-- the name of each tool, its duration, whether it failed, or why it did not run.
-- refused_actions keeps its name and now counts the calls the agent did not run,
-- the writes the model asked for in an unattended run: the column is not renamed.
-- pending_schedule_id is the schedule of a queued or running run, NULL once it
-- ended: its unique key keeps one such run per schedule, whatever the requests
-- asking for one at the same time.
ALTER TABLE ai_schedule_runs
  ADD COLUMN IF NOT EXISTS request_id VARCHAR(64) NOT NULL DEFAULT '' AFTER error,
  ADD COLUMN IF NOT EXISTS calls MEDIUMTEXT NULL AFTER request_id,
  ADD COLUMN IF NOT EXISTS pending_schedule_id INT
    AS (CASE WHEN state IN ('queued', 'running') THEN schedule_id END) PERSISTENT,
  ADD UNIQUE KEY IF NOT EXISTS uk_ai_schedule_runs_pending (pending_schedule_id);
