-- 2026-11-05: the alert notifications. A row tells that an alert of the
-- dashboard was sent, or is being sent, or failed to be, to a recipient: a
-- user's mail box ("email:user:<id>") for now, a channel later. It replaces
-- alerts_sent, whose sent mails are imported so they are not sent again. The
-- rows go with their alert, purged when it leaves the dashboard.
--
-- state: pending while the message is being sent, claimed before the sending
-- so a scheduler restarted meanwhile does not send it twice in a row; sent;
-- failed, tried again at next_try until attempts reaches the maximum.
CREATE TABLE IF NOT EXISTS notification_deliveries (
  id bigint NOT NULL AUTO_INCREMENT,
  alert_id int(11) NOT NULL,
  recipient varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  state enum('pending','sent','failed') NOT NULL DEFAULT 'pending',
  attempts tinyint unsigned NOT NULL DEFAULT 0,
  last_error varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL DEFAULT '',
  next_try datetime DEFAULT NULL,
  created timestamp NOT NULL DEFAULT current_timestamp(),
  updated timestamp NOT NULL DEFAULT current_timestamp() ON UPDATE current_timestamp(),
  PRIMARY KEY (id),
  UNIQUE KEY uk_recipient_alert (recipient, alert_id),
  KEY k_alert_id (alert_id),
  KEY k_state_next_try (state, next_try)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci;

INSERT IGNORE INTO notification_deliveries (alert_id, recipient, state, attempts, created, updated)
  SELECT alert_id, CONCAT('email:user:', user_id), 'sent', 1, sent, sent
  FROM alerts_sent
  WHERE msg_type = 'email' AND alert_id IN (SELECT id FROM dashboard);
