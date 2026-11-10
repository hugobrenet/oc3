package cdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type (
	// NotifyUser is a user to notify of the new alerts by mail, with the
	// settings of their account (auth_user, user_prefs).
	NotifyUser struct {
		ID    int64
		Email string
		// LogLevel is the lowest level notified: debug, info, warning, error
		// or critical.
		LogLevel string
		// Delay is the age an alert reaches before it is notified, so that the
		// alerts gone by themselves meanwhile are not.
		Delay time.Duration
		// Prefs is the preferences document of the user, holding the
		// notification periods.
		Prefs string
	}

	// NotifyAlert is an alert of the dashboard to notify, with the names of
	// its node and of its service.
	NotifyAlert struct {
		ID       int64
		Created  time.Time
		Severity int
		Type     string
		Fmt      string
		Dict     string
		Env      string
		NodeID   string
		SvcID    string
		Nodename string
		Svcname  string
		App      string
		// Attempts is the number of tries already made, 0 for an alert never
		// sent to the recipient.
		Attempts int
		// Object and Message, when set, are what a message said of an alert
		// gone since, its resolution announced: neither its names nor its
		// format are in the dashboard any more.
		Object  string
		Message string
	}
)

// NotifyEmailUsers returns the users asking for the alerts by mail: those
// with a mail address and the mail notifications on (auth_user, as
// init/alertd/alertd.py get_users() reads them).
func (oDb *DB) NotifyEmailUsers(ctx context.Context) ([]NotifyUser, error) {
	const query = "SELECT u.id, u.email, COALESCE(u.email_log_level, 'warning')," +
		" COALESCE(u.email_notifications_delay, 0), COALESCE(p.prefs, '')" +
		" FROM auth_user u LEFT JOIN user_prefs p ON p.user_id = u.id" +
		" WHERE u.email_notifications = 'T' AND u.email IS NOT NULL AND u.email <> ''" +
		" AND COALESCE(u.registration_key, '') = ''"
	rows, err := oDb.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("notifyEmailUsers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var l []NotifyUser
	for rows.Next() {
		var u NotifyUser
		var delay int64
		if err := rows.Scan(&u.ID, &u.Email, &u.LogLevel, &delay, &u.Prefs); err != nil {
			return nil, fmt.Errorf("notifyEmailUsers: %w", err)
		}
		if delay < 0 {
			delay = 0
		}
		u.Delay = time.Duration(delay) * time.Minute
		l = append(l, u)
	}
	return l, rows.Err()
}

// NotifyAlertsForUser returns the alerts of the dashboard to send to the
// recipient on behalf of the user userID, the most severe first, at most
// limit:
//
//   - on a node or a service of an app the user is responsible for, through
//     one of their groups: a user responsible for nothing receives nothing;
//   - of minSeverity or more, created delay ago or more;
//   - neither on a node nor on a service whose notifications are off or
//     snoozed;
//   - never sent to the recipient, or failed and due to be tried again
//     (next_try passed, fewer than maxAttempts tries), or pending for longer
//     than stalePending: the scheduler sending it stopped meanwhile.
func (oDb *DB) NotifyAlertsForUser(ctx context.Context, userID int64, recipient string, minSeverity int, delay time.Duration, maxAttempts int, stalePending time.Duration, limit int) ([]NotifyAlert, error) {
	const apps = "SELECT a.app FROM apps a" +
		" JOIN apps_responsibles ar ON ar.app_id = a.id" +
		" JOIN auth_membership am ON am.group_id = ar.group_id" +
		" WHERE am.user_id = ?"
	scope := "(n.app IN (" + apps + ") OR s.svc_app IN (" + apps + "))"
	return oDb.notifyAlerts(ctx, scope, []any{userID, userID}, recipient, minSeverity, delay, maxAttempts, stalePending, limit)
}

// NotifyFilter restricts the alerts of a subscription: to some types, or all
// but some, to some environments, to the nodes and services of a filterset.
type NotifyFilter struct {
	TypesInclude []string
	TypesExclude []string
	Envs         []string
	FiltersetID  int
}

// NotifyAlertsForGroup returns the alerts of the dashboard to send to the
// recipient for the group groupID, as NotifyAlertsForUser does for a user:
// those of the apps the group is responsible for, or, with all, every alert,
// and only those passing the filter.
func (oDb *DB) NotifyAlertsForGroup(ctx context.Context, groupID int64, all bool, filter NotifyFilter, recipient string, minSeverity int, delay time.Duration, maxAttempts int, stalePending time.Duration, limit int) ([]NotifyAlert, error) {
	conds := []string{"1=1"}
	var args []any
	if !all {
		const apps = "SELECT a.app FROM apps a" +
			" JOIN apps_responsibles ar ON ar.app_id = a.id" +
			" WHERE ar.group_id = ?"
		conds = append(conds, "(n.app IN ("+apps+") OR s.svc_app IN ("+apps+"))")
		args = append(args, groupID, groupID)
	}
	if len(filter.TypesInclude) > 0 {
		conds = append(conds, "d.dash_type IN ("+Placeholders(len(filter.TypesInclude))+")")
		for _, t := range filter.TypesInclude {
			args = append(args, t)
		}
	}
	if len(filter.TypesExclude) > 0 {
		conds = append(conds, "d.dash_type NOT IN ("+Placeholders(len(filter.TypesExclude))+")")
		for _, t := range filter.TypesExclude {
			args = append(args, t)
		}
	}
	if len(filter.Envs) > 0 {
		conds = append(conds, "d.dash_env IN ("+Placeholders(len(filter.Envs))+")")
		for _, e := range filter.Envs {
			args = append(args, e)
		}
	}
	if filter.FiltersetID != 0 {
		// The ids of the nodes and of the services of the filterset, as the
		// lists filtered by it select them; a filterset selecting nothing
		// lets no alert through.
		var parts []string
		for _, ref := range []struct{ field, col string }{{"node_id", "d.node_id"}, {"svc_id", "d.svc_id"}} {
			q, qArgs, err := filtersetQuery(ctx, oDb.GetVFiltersets, filter.FiltersetID, ref.field, map[int]bool{})
			if err != nil {
				return nil, fmt.Errorf("notifyAlertsForGroup: filterset %d: %w", filter.FiltersetID, err)
			}
			if q == "" {
				continue
			}
			parts = append(parts, ref.col+" IN (SELECT * FROM ("+q+") fs_"+ref.field+")")
			args = append(args, qArgs...)
		}
		if len(parts) == 0 {
			return nil, nil
		}
		conds = append(conds, "("+strings.Join(parts, " OR ")+")")
	}
	return oDb.notifyAlerts(ctx, strings.Join(conds, " AND "), args, recipient, minSeverity, delay, maxAttempts, stalePending, limit)
}

func (oDb *DB) notifyAlerts(ctx context.Context, scope string, scopeArgs []any, recipient string, minSeverity int, delay time.Duration, maxAttempts int, stalePending time.Duration, limit int) ([]NotifyAlert, error) {
	query := "SELECT d.id, d.dash_created, d.dash_severity, d.dash_type," +
		" COALESCE(d.dash_fmt, ''), COALESCE(d.dash_dict, ''), COALESCE(d.dash_env, '')," +
		" COALESCE(d.node_id, ''), COALESCE(d.svc_id, ''), COALESCE(n.nodename, ''), COALESCE(s.svcname, '')," +
		" COALESCE(NULLIF(s.svc_app, ''), n.app, ''), COALESCE(nd.attempts, 0)" +
		" FROM dashboard d" +
		" LEFT JOIN nodes n ON n.node_id = d.node_id" +
		" LEFT JOIN services s ON s.svc_id = d.svc_id" +
		" LEFT JOIN notification_deliveries nd ON nd.alert_id = d.id AND nd.recipient = ?" +
		" WHERE d.dash_severity >= ?" +
		" AND d.dash_created <= NOW() - INTERVAL ? SECOND" +
		" AND (n.node_id IS NULL OR (COALESCE(n.notifications, 'T') = 'T' AND (n.snooze_till IS NULL OR n.snooze_till < NOW())))" +
		" AND (s.svc_id IS NULL OR (COALESCE(s.svc_notifications, 'T') = 'T' AND (s.svc_snooze_till IS NULL OR s.svc_snooze_till < NOW())))" +
		" AND " + scope +
		" AND (nd.id IS NULL" +
		"  OR (nd.state = 'failed' AND nd.attempts < ? AND nd.next_try <= NOW())" +
		"  OR (nd.state = 'pending' AND nd.updated < NOW() - INTERVAL ? SECOND AND nd.attempts < ?))" +
		" ORDER BY d.dash_severity DESC, d.id" +
		" LIMIT ?"
	args := []any{recipient, minSeverity, int64(delay.Seconds())}
	args = append(args, scopeArgs...)
	args = append(args, maxAttempts, int64(stalePending.Seconds()), maxAttempts, limit)
	rows, err := oDb.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("notifyAlerts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var l []NotifyAlert
	for rows.Next() {
		var a NotifyAlert
		var created sql.NullTime
		if err := rows.Scan(&a.ID, &created, &a.Severity, &a.Type, &a.Fmt, &a.Dict, &a.Env,
			&a.NodeID, &a.SvcID, &a.Nodename, &a.Svcname, &a.App, &a.Attempts); err != nil {
			return nil, fmt.Errorf("notifyAlerts: %w", err)
		}
		a.Created = created.Time
		l = append(l, a)
	}
	return l, rows.Err()
}

// NotifyClaimItem is an alert claimed for a recipient, with what the message
// announces of it, kept to announce its resolution.
type NotifyClaimItem struct {
	ID       int64
	Severity int
	Type     string
	Object   string
	Message  string
}

// NotifyClaim records the alerts as being sent to the recipient, before the
// sending: a try more for each.
func (oDb *DB) NotifyClaim(ctx context.Context, recipient string, items []NotifyClaimItem) error {
	if len(items) == 0 {
		return nil
	}
	values := make([]string, len(items))
	args := make([]any, 0, 6*len(items))
	for i, it := range items {
		values[i] = "(?, ?, 'pending', 1, ?, ?, ?, ?)"
		args = append(args, it.ID, recipient, it.Severity, truncate(it.Type, 60), truncate(it.Object, 255), truncate(it.Message, 512))
	}
	query := "INSERT INTO notification_deliveries (alert_id, recipient, state, attempts, alert_severity, alert_type, alert_object, alert_message) VALUES " +
		strings.Join(values, ", ") +
		" ON DUPLICATE KEY UPDATE state = 'pending', attempts = attempts + 1, next_try = NULL," +
		" alert_severity = VALUES(alert_severity), alert_type = VALUES(alert_type)," +
		" alert_object = VALUES(alert_object), alert_message = VALUES(alert_message)"
	if _, err := oDb.DB.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("notifyClaim: %w", err)
	}
	return nil
}

// truncate cuts s to n runes at most.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// NotifyResolvedAlert is an alert sent to a recipient and gone from the
// dashboard since, as the message announced it.
type NotifyResolvedAlert struct {
	ID       int64
	Severity int
	Type     string
	Object   string
	Message  string
}

// NotifyResolved returns the alerts sent to the recipient and gone from the
// dashboard since, the most severe first, at most limit.
func (oDb *DB) NotifyResolved(ctx context.Context, recipient string, limit int) ([]NotifyResolvedAlert, error) {
	rows, err := oDb.DB.QueryContext(ctx,
		"SELECT nd.alert_id, COALESCE(nd.alert_severity, 0), COALESCE(nd.alert_type, ''), COALESCE(nd.alert_object, ''), COALESCE(nd.alert_message, '')"+
			" FROM notification_deliveries nd LEFT JOIN dashboard d ON d.id = nd.alert_id"+
			" WHERE nd.recipient = ? AND nd.state = 'sent' AND d.id IS NULL"+
			" ORDER BY nd.alert_severity DESC, nd.alert_id LIMIT ?", recipient, limit)
	if err != nil {
		return nil, fmt.Errorf("notifyResolved: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var l []NotifyResolvedAlert
	for rows.Next() {
		var a NotifyResolvedAlert
		if err := rows.Scan(&a.ID, &a.Severity, &a.Type, &a.Object, &a.Message); err != nil {
			return nil, fmt.Errorf("notifyResolved: %w", err)
		}
		l = append(l, a)
	}
	return l, rows.Err()
}

// NotifyForget deletes the deliveries of the alerts to the recipient, once
// their resolution announced.
func (oDb *DB) NotifyForget(ctx context.Context, recipient string, alertIDs []int64) error {
	if len(alertIDs) == 0 {
		return nil
	}
	args := []any{recipient}
	for _, id := range alertIDs {
		args = append(args, id)
	}
	if _, err := oDb.DB.ExecContext(ctx, "DELETE FROM notification_deliveries WHERE recipient = ? AND alert_id IN ("+Placeholders(len(alertIDs))+")", args...); err != nil {
		return fmt.Errorf("notifyForget: %w", err)
	}
	return nil
}

// NotifyDone records the outcome of the sending of the alerts to the
// recipient: sent when sendErr is nil, else failed, to be tried again after
// retryAfter(attempts).
func (oDb *DB) NotifyDone(ctx context.Context, recipient string, alertIDs []int64, sendErr error, retryAfter func(attempts int) time.Duration) error {
	if len(alertIDs) == 0 {
		return nil
	}
	in := Placeholders(len(alertIDs))
	args := make([]any, 0, len(alertIDs)+4)
	var query string
	if sendErr == nil {
		query = "UPDATE notification_deliveries SET state = 'sent', last_error = '', next_try = NULL" +
			" WHERE recipient = ? AND alert_id IN (" + in + ")"
		args = append(args, recipient)
	} else {
		msg := sendErr.Error()
		if len(msg) > 255 {
			msg = msg[:255]
		}
		// The delay grows with the tries; one statement for all the alerts
		// of the message, which were tried as many times but for those
		// added to it since: the delay of the fewest tries is taken.
		query = "UPDATE notification_deliveries SET state = 'failed', last_error = ?," +
			" next_try = NOW() + INTERVAL CASE" +
			" WHEN attempts <= 1 THEN ? WHEN attempts = 2 THEN ? ELSE ? END SECOND" +
			" WHERE recipient = ? AND alert_id IN (" + in + ")"
		args = append(args, msg,
			int64(retryAfter(1).Seconds()), int64(retryAfter(2).Seconds()), int64(retryAfter(3).Seconds()),
			recipient)
	}
	for _, id := range alertIDs {
		args = append(args, id)
	}
	if _, err := oDb.DB.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("notifyDone: %w", err)
	}
	return nil
}

// NotifyPurge deletes the deliveries of the alerts gone from the dashboard,
// and of the users and subscriptions gone, and the alerts_sent rows the
// historical collector left of the alerts gone.
func (oDb *DB) NotifyPurge(ctx context.Context) (int64, error) {
	var n int64
	for _, query := range []string{
		// Kept: the deliveries sent for a subscription announcing the
		// resolutions, until it announces them, two days at most.
		"DELETE nd FROM notification_deliveries nd LEFT JOIN dashboard d ON d.id = nd.alert_id" +
			" WHERE d.id IS NULL AND NOT (nd.state = 'sent' AND nd.updated > NOW() - INTERVAL 2 DAY" +
			" AND nd.recipient IN (SELECT CONCAT('subscription:', id) FROM notification_subscriptions" +
			" WHERE notify_resolved = 1 AND enabled = 1))",
		"DELETE FROM notification_deliveries WHERE recipient LIKE 'email:user:%'" +
			" AND CAST(SUBSTRING(recipient, 12) AS UNSIGNED) NOT IN (SELECT id FROM auth_user)",
		"DELETE FROM notification_deliveries WHERE recipient LIKE 'subscription:%'" +
			" AND CAST(SUBSTRING(recipient, 14) AS UNSIGNED) NOT IN (SELECT id FROM notification_subscriptions)",
		"DELETE s FROM alerts_sent s LEFT JOIN dashboard d ON d.id = s.alert_id WHERE d.id IS NULL",
	} {
		result, err := oDb.DB.ExecContext(ctx, query)
		if err != nil {
			return n, fmt.Errorf("notifyPurge: %w", err)
		}
		if count, err := result.RowsAffected(); err == nil {
			n += count
		}
	}
	return n, nil
}

// ExpireSnoozes ends the snoozes of the nodes and of the services past their
// deadline, as init/alertd/alertd.py janitor_snooze() does.
func (oDb *DB) ExpireSnoozes(ctx context.Context) (int64, error) {
	var n int64
	for _, query := range []string{
		"UPDATE nodes SET snooze_till = NULL WHERE snooze_till < NOW()",
		"UPDATE services SET svc_snooze_till = NULL WHERE svc_snooze_till < NOW()",
	} {
		result, err := oDb.DB.ExecContext(ctx, query)
		if err != nil {
			return n, fmt.Errorf("expireSnoozes: %w", err)
		}
		if count, err := result.RowsAffected(); err == nil {
			n += count
		}
	}
	return n, nil
}

// UserNotifySettings are the mail notification settings of a user.
type UserNotifySettings struct {
	Email    string
	Enabled  bool
	LogLevel string
	DelayMin int
	// Prefs is the preferences document of the user, holding the periods.
	Prefs string
}

// UserNotifySettingsOf returns the settings of the user, found false when the
// user is unknown.
func (oDb *DB) UserNotifySettingsOf(ctx context.Context, userID int64) (UserNotifySettings, bool, error) {
	var s UserNotifySettings
	var enabled string
	err := oDb.DB.QueryRowContext(ctx,
		"SELECT COALESCE(u.email, ''), COALESCE(u.email_notifications, 'T'), COALESCE(u.email_log_level, 'warning'),"+
			" COALESCE(u.email_notifications_delay, 0), COALESCE(p.prefs, '')"+
			" FROM auth_user u LEFT JOIN user_prefs p ON p.user_id = u.id WHERE u.id = ?", userID).
		Scan(&s.Email, &enabled, &s.LogLevel, &s.DelayMin, &s.Prefs)
	if errors.Is(err, sql.ErrNoRows) {
		return s, false, nil
	}
	if err != nil {
		return s, false, fmt.Errorf("userNotifySettingsOf: %w", err)
	}
	s.Enabled = enabled == "T"
	return s, true, nil
}

// SetUserNotifySettings writes the mail notification settings of the user,
// the periods in their preferences document, prefs, as given.
func (oDb *DB) SetUserNotifySettings(ctx context.Context, userID int64, s UserNotifySettings) error {
	enabled := "F"
	if s.Enabled {
		enabled = "T"
	}
	if _, err := oDb.DB.ExecContext(ctx,
		"UPDATE auth_user SET email_notifications = ?, email_log_level = ?, email_notifications_delay = ? WHERE id = ?",
		enabled, s.LogLevel, s.DelayMin, userID); err != nil {
		return fmt.Errorf("setUserNotifySettings: %w", err)
	}
	oDb.SetChange("auth_user")
	return oDb.SaveUserPrefs(ctx, userID, s.Prefs)
}

// notifyDeliveriesFrom is the FROM clause of the delivery lists: a derived
// table named after notification_deliveries, where each delivery carries what
// names its recipient, the channel and the group of a subscription or the mail
// address of a user, and whether its alert is still in the dashboard, so that
// they can be selected, filtered and sorted like the other columns.
const notifyDeliveriesFrom = ` FROM (
	SELECT nd.id, nd.alert_id, nd.recipient,
		IF(nd.recipient LIKE 'subscription:%', 'subscription', 'user') AS recipient_kind,
		COALESCE(CONCAT(c.name, ' / ', COALESCE(g.role, '?')), u.email, nd.recipient) AS recipient_label,
		COALESCE(c.name, '') AS channel_name, COALESCE(g.role, '') AS group_role,
		nd.state, nd.attempts, nd.last_error, nd.next_try, nd.alert_severity,
		COALESCE(nd.alert_type, '') AS alert_type, COALESCE(nd.alert_object, '') AS alert_object,
		COALESCE(nd.alert_message, '') AS alert_message,
		IF(d.id IS NULL, 0, 1) AS alert_present, nd.created, nd.updated
	FROM notification_deliveries nd
	LEFT JOIN dashboard d ON d.id = nd.alert_id
	LEFT JOIN notification_subscriptions s ON nd.recipient LIKE 'subscription:%' AND s.id = CAST(SUBSTRING(nd.recipient, 14) AS UNSIGNED)
	LEFT JOIN notification_channels c ON c.id = s.channel_id
	LEFT JOIN auth_group g ON g.id = s.group_id
	LEFT JOIN auth_user u ON nd.recipient LIKE 'email:user:%' AND u.id = CAST(SUBSTRING(nd.recipient, 12) AS UNSIGNED)
) notification_deliveries`

// GetNotifyDeliveries returns the deliveries, with the column filters, sort and
// pagination of the request, the latest first by default.
func (oDb *DB) GetNotifyDeliveries(ctx context.Context, p ListParams) ([]map[string]any, error) {
	if len(p.SelectExprs) == 0 {
		return nil, fmt.Errorf("getNotifyDeliveries: no select expressions")
	}
	query := "SELECT " + strings.Join(p.SelectExprs, ", ") + notifyDeliveriesFrom + " WHERE notification_deliveries.id > 0"
	filterConds, args := p.FilterConditions()
	for _, cond := range filterConds {
		query += " AND " + cond
	}
	if gb := p.GroupByClause(""); gb != "" {
		query += " " + gb
	}
	query += " " + p.OrderByClause("notification_deliveries.updated DESC, notification_deliveries.id DESC")
	query, args = appendLimitOffset(query, args, p.Limit, p.Offset)
	rows, err := oDb.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getNotifyDeliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRowsToMaps(rows, p.Props, p.TypeHints)
}
