-- 2026-11-05: the filters of the subscriptions, and the notice of the alerts
-- resolved.
--
-- A subscription may restrict its alerts to some types (types_include, JSON
-- list, empty for all) or leave some out (types_exclude), to some environments
-- of the alerts (envs, JSON list, empty for all), and to the nodes and services
-- of a filterset (filterset_id). It sends on the periods of the week it lists
-- (periods_json, as the notifications_periods of the user preferences, empty
-- for always). With notify_resolved, the alerts it sent are announced again
-- once gone from the dashboard.
ALTER TABLE notification_subscriptions
  ADD COLUMN IF NOT EXISTS types_include TEXT NULL,
  ADD COLUMN IF NOT EXISTS types_exclude TEXT NULL,
  ADD COLUMN IF NOT EXISTS envs TEXT NULL,
  ADD COLUMN IF NOT EXISTS filterset_id INT NULL,
  ADD COLUMN IF NOT EXISTS periods_json TEXT NULL,
  ADD COLUMN IF NOT EXISTS notify_resolved TINYINT(1) NOT NULL DEFAULT 0;

-- What a delivery announced, kept to announce the resolution once the alert
-- is gone from the dashboard: its severity, type, object and message.
ALTER TABLE notification_deliveries
  ADD COLUMN IF NOT EXISTS alert_severity TINYINT NULL,
  ADD COLUMN IF NOT EXISTS alert_type VARCHAR(60) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NULL,
  ADD COLUMN IF NOT EXISTS alert_object VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NULL,
  ADD COLUMN IF NOT EXISTS alert_message VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NULL;
