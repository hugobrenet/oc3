-- 2026-11-05: the channels of the alert notifications, and the subscriptions of
-- the groups to them. A channel is a destination an AlertsManager declares: a
-- list of mail addresses, or a Slack, Mattermost, Teams (Workflows) or generic
-- webhook. Its secrets (the webhook url, which is the credential, and the key
-- signing the generic webhooks) are one JSON object sealed with the safe master
-- key, as those of the form connectors.
CREATE TABLE IF NOT EXISTS notification_channels (
  id INT NOT NULL AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL,
  type ENUM('email','slack','mattermost','teams','webhook') NOT NULL,
  description TEXT NOT NULL,
  settings_json TEXT NOT NULL,
  enabled TINYINT(1) NOT NULL DEFAULT 1,
  secrets MEDIUMBLOB NULL,
  secrets_key_id VARCHAR(64) NULL,
  secrets_wrapped_key VARBINARY(512) NULL,
  updated_by VARCHAR(255) NOT NULL DEFAULT '',
  created DATETIME NOT NULL,
  updated DATETIME NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_notification_channels_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci;

-- A subscription sends to a channel the alerts of a group: those of the apps
-- the group is responsible for (scope responsible), or all of them (scope all),
-- from a severity, once they are delay_min minutes old. Its deliveries are
-- recorded in notification_deliveries as the recipient "subscription:<id>".
CREATE TABLE IF NOT EXISTS notification_subscriptions (
  id INT NOT NULL AUTO_INCREMENT,
  channel_id INT NOT NULL,
  group_id INT NOT NULL,
  scope ENUM('responsible','all') NOT NULL DEFAULT 'responsible',
  min_severity TINYINT NOT NULL DEFAULT 1,
  delay_min INT NOT NULL DEFAULT 0,
  enabled TINYINT(1) NOT NULL DEFAULT 1,
  updated_by VARCHAR(255) NOT NULL DEFAULT '',
  created DATETIME NOT NULL,
  updated DATETIME NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_notification_subscriptions (channel_id, group_id),
  KEY k_notification_subscriptions_group (group_id),
  CONSTRAINT fk_notification_subscriptions_channel FOREIGN KEY (channel_id)
    REFERENCES notification_channels (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci;
