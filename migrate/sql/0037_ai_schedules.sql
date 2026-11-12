-- 2026-11-12: the schedules of the skills of the AI assistant. A schedule runs
-- a skill of the oc3 backend at its time, on the days of the week it lists, in
-- its time zone, and sends the answer to the notification channels it names:
-- the collector asks the agent, as the account the scheduler is configured
-- with, and sends the answer itself, the model choosing neither the time nor
-- the destination. next_run is when it runs next, NULL when disabled.
-- channel_ids is a JSON list of notification_channels ids.
CREATE TABLE IF NOT EXISTS ai_schedules (
  id INT NOT NULL AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL,
  skill VARCHAR(64) NOT NULL,
  prompt TEXT NOT NULL,
  days VARCHAR(32) NOT NULL,
  at_time CHAR(5) NOT NULL,
  timezone VARCHAR(64) NOT NULL,
  channel_ids TEXT NOT NULL,
  send_failures TINYINT(1) NOT NULL DEFAULT 1,
  enabled TINYINT(1) NOT NULL DEFAULT 1,
  next_run DATETIME NULL,
  created DATETIME NOT NULL,
  updated DATETIME NOT NULL,
  updated_by VARCHAR(255) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY uk_ai_schedules_name (name),
  KEY k_ai_schedules_next_run (enabled, next_run)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
