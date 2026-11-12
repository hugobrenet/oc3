-- 2026-11-12: the runs of the schedules of the AI assistant, at their time or
-- asked at once from the collector (trigger manual). state: queued until the
-- scheduler takes it, running, then sent once every channel accepted the
-- report, failed when the agent gave no answer, unsent when a channel refused
-- it after its attempts. report is the answer sent; error, why it was not.
CREATE TABLE IF NOT EXISTS ai_schedule_runs (
  id BIGINT NOT NULL AUTO_INCREMENT,
  schedule_id INT NOT NULL,
  run_trigger ENUM('schedule','manual') NOT NULL,
  state ENUM('queued','running','sent','failed','unsent') NOT NULL DEFAULT 'queued',
  queued DATETIME NOT NULL,
  started DATETIME NULL,
  ended DATETIME NULL,
  tool_calls INT NOT NULL DEFAULT 0,
  refused_actions INT NOT NULL DEFAULT 0,
  report MEDIUMTEXT NOT NULL,
  error VARCHAR(1024) NOT NULL DEFAULT '',
  queued_by VARCHAR(255) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY k_ai_schedule_runs_schedule (schedule_id, id),
  KEY k_ai_schedule_runs_state (state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
