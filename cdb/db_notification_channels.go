package cdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type (
	// NotifyChannel is a row of notification_channels: a destination of the
	// alert notifications. Secrets holds the sealed JSON object of its
	// secrets, nil when it has none.
	NotifyChannel struct {
		ID          int64
		Name        string
		Type        string
		Description string
		Settings    NotifyChannelSettings
		Enabled     bool
		Secrets     *SealedSecret
		UpdatedBy   string
		Created     time.Time
		Updated     time.Time
	}

	// NotifyChannelSettings are the settings of a channel which are no secret.
	NotifyChannelSettings struct {
		// Addresses are the mail addresses of an email channel.
		Addresses []string `json:"addresses,omitempty"`
		// Username and IconURL are the name and the picture a Slack or a
		// Mattermost webhook shows, when it lets the sender choose them.
		Username string `json:"username,omitempty"`
		IconURL  string `json:"icon_url,omitempty"`
		// Networks are the private networks the webhook host may resolve
		// to, as CIDR prefixes: the public addresses are always allowed.
		Networks []string `json:"networks,omitempty"`
	}

	// NotifySubscription is a row of notification_subscriptions: the alerts
	// of a group sent to a channel.
	NotifySubscription struct {
		ID          int64
		ChannelID   int64
		GroupID     int64
		Scope       string
		MinSeverity int
		DelayMin    int
		Enabled     bool
		// TypesInclude, TypesExclude, Envs and FiltersetID filter the alerts,
		// see NotifyFilter; 0 for no filterset.
		TypesInclude []string
		TypesExclude []string
		Envs         []string
		FiltersetID  int
		// Periods is the JSON object of the periods of the week the
		// subscription sends on, as the notifications_periods of the user
		// preferences; empty for always.
		Periods string
		// NotifyResolved announces the alerts sent once they are resolved.
		NotifyResolved bool
		UpdatedBy      string
		Created        time.Time
		Updated        time.Time
		// GroupRole and ChannelName are read along, for the display.
		GroupRole   string
		ChannelName string
	}
)

const notifyChannelColumns = "id, name, type, description, settings_json, enabled," +
	" secrets, secrets_key_id, secrets_wrapped_key, updated_by, created, updated"

func scanNotifyChannel(scan func(...any) error) (*NotifyChannel, error) {
	var (
		c                NotifyChannel
		settings         string
		secrets, wrapped []byte
		keyID            sql.NullString
	)
	if err := scan(&c.ID, &c.Name, &c.Type, &c.Description, &settings, &c.Enabled,
		&secrets, &keyID, &wrapped, &c.UpdatedBy, &c.Created, &c.Updated); err != nil {
		return nil, err
	}
	if settings != "" {
		if err := json.Unmarshal([]byte(settings), &c.Settings); err != nil {
			return nil, fmt.Errorf("notification channel %d: %w", c.ID, err)
		}
	}
	if len(secrets) > 0 && keyID.Valid {
		c.Secrets = &SealedSecret{Content: secrets, KeyID: keyID.String, WrappedKey: wrapped}
	}
	return &c, nil
}

// NotifyChannels lists the channels, by name.
func (oDb *DB) NotifyChannels(ctx context.Context) ([]NotifyChannel, error) {
	rows, err := oDb.DB.QueryContext(ctx, "SELECT "+notifyChannelColumns+" FROM notification_channels ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("notifyChannels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []NotifyChannel
	for rows.Next() {
		c, err := scanNotifyChannel(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// NotifyChannelByID returns the channel, nil when there is none.
func (oDb *DB) NotifyChannelByID(ctx context.Context, id int64) (*NotifyChannel, error) {
	row := oDb.DB.QueryRowContext(ctx, "SELECT "+notifyChannelColumns+" FROM notification_channels WHERE id = ?", id)
	c, err := scanNotifyChannel(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// NotifyChannelNameTaken tells whether another channel than id has the name.
func (oDb *DB) NotifyChannelNameTaken(ctx context.Context, name string, id int64) (bool, error) {
	var n int
	err := oDb.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM notification_channels WHERE name = ? AND id <> ?", name, id).Scan(&n)
	return n > 0, err
}

// InsertNotifyChannel creates a channel, without its secrets, sealed under
// the label of its id once known, and returns the id.
func (oDb *DB) InsertNotifyChannel(ctx context.Context, c NotifyChannel) (int64, error) {
	settings, err := jsonText(c.Settings)
	if err != nil {
		return 0, err
	}
	result, err := oDb.DB.ExecContext(ctx,
		"INSERT INTO notification_channels (name, type, description, settings_json, enabled, updated_by, created, updated)"+
			" VALUES (?, ?, ?, ?, ?, ?, NOW(), NOW())",
		c.Name, c.Type, c.Description, settings, c.Enabled, c.UpdatedBy)
	if err != nil {
		return 0, fmt.Errorf("insertNotifyChannel: %w", err)
	}
	oDb.SetChange("notification_channels")
	return result.LastInsertId()
}

// UpdateNotifyChannel writes the channel c.ID, its secrets included.
func (oDb *DB) UpdateNotifyChannel(ctx context.Context, c NotifyChannel) (bool, error) {
	settings, err := jsonText(c.Settings)
	if err != nil {
		return false, err
	}
	var secrets, wrapped []byte
	var keyID sql.NullString
	if c.Secrets != nil {
		secrets, wrapped = c.Secrets.Content, c.Secrets.WrappedKey
		keyID = sql.NullString{String: c.Secrets.KeyID, Valid: true}
	}
	result, err := oDb.DB.ExecContext(ctx,
		"UPDATE notification_channels SET name = ?, type = ?, description = ?, settings_json = ?, enabled = ?,"+
			" secrets = ?, secrets_key_id = ?, secrets_wrapped_key = ?, updated_by = ?, updated = NOW() WHERE id = ?",
		c.Name, c.Type, c.Description, settings, c.Enabled, secrets, keyID, wrapped, c.UpdatedBy, c.ID)
	if err != nil {
		return false, fmt.Errorf("updateNotifyChannel: %w", err)
	}
	oDb.SetChange("notification_channels")
	n, err := result.RowsAffected()
	return n > 0, err
}

// DeleteNotifyChannel deletes a channel and its subscriptions.
func (oDb *DB) DeleteNotifyChannel(ctx context.Context, id int64) (bool, error) {
	if _, err := oDb.DB.ExecContext(ctx, "DELETE FROM notification_subscriptions WHERE channel_id = ?", id); err != nil {
		return false, fmt.Errorf("deleteNotifyChannel: %w", err)
	}
	result, err := oDb.DB.ExecContext(ctx, "DELETE FROM notification_channels WHERE id = ?", id)
	if err != nil {
		return false, fmt.Errorf("deleteNotifyChannel: %w", err)
	}
	oDb.SetChange("notification_channels")
	n, err := result.RowsAffected()
	return n > 0, err
}

const notifySubscriptionColumns = "s.id, s.channel_id, s.group_id, s.scope, s.min_severity, s.delay_min, s.enabled," +
	" COALESCE(s.types_include, ''), COALESCE(s.types_exclude, ''), COALESCE(s.envs, ''), COALESCE(s.filterset_id, 0)," +
	" COALESCE(s.periods_json, ''), s.notify_resolved," +
	" s.updated_by, s.created, s.updated, COALESCE(g.role, ''), c.name"

const notifySubscriptionFrom = " FROM notification_subscriptions s" +
	" JOIN notification_channels c ON c.id = s.channel_id" +
	" LEFT JOIN auth_group g ON g.id = s.group_id"

func scanNotifySubscription(scan func(...any) error) (*NotifySubscription, error) {
	var s NotifySubscription
	var include, exclude, envs string
	if err := scan(&s.ID, &s.ChannelID, &s.GroupID, &s.Scope, &s.MinSeverity, &s.DelayMin, &s.Enabled,
		&include, &exclude, &envs, &s.FiltersetID, &s.Periods, &s.NotifyResolved,
		&s.UpdatedBy, &s.Created, &s.Updated, &s.GroupRole, &s.ChannelName); err != nil {
		return nil, err
	}
	for _, f := range []struct {
		raw string
		dst *[]string
	}{{include, &s.TypesInclude}, {exclude, &s.TypesExclude}, {envs, &s.Envs}} {
		if f.raw == "" {
			continue
		}
		if err := json.Unmarshal([]byte(f.raw), f.dst); err != nil {
			return nil, fmt.Errorf("notification subscription %d: %w", s.ID, err)
		}
	}
	return &s, nil
}

// subscriptionFilterArgs returns the filter columns of s to write.
func subscriptionFilterArgs(s NotifySubscription) ([]any, error) {
	var out []any
	for _, l := range [][]string{s.TypesInclude, s.TypesExclude, s.Envs} {
		if len(l) == 0 {
			out = append(out, nil)
			continue
		}
		text, err := jsonText(l)
		if err != nil {
			return nil, err
		}
		out = append(out, text)
	}
	var fset, periods any
	if s.FiltersetID != 0 {
		fset = s.FiltersetID
	}
	if s.Periods != "" {
		periods = s.Periods
	}
	return append(out, fset, periods, s.NotifyResolved), nil
}

// NotifySubscriptions lists the subscriptions, of the channel channelID when
// not 0, by channel and group.
func (oDb *DB) NotifySubscriptions(ctx context.Context, channelID int64) ([]NotifySubscription, error) {
	query := "SELECT " + notifySubscriptionColumns + notifySubscriptionFrom
	var args []any
	if channelID != 0 {
		query += " WHERE s.channel_id = ?"
		args = append(args, channelID)
	}
	query += " ORDER BY c.name, g.role, s.id"
	rows, err := oDb.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("notifySubscriptions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []NotifySubscription
	for rows.Next() {
		s, err := scanNotifySubscription(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// NotifySubscriptionByID returns the subscription, nil when there is none.
func (oDb *DB) NotifySubscriptionByID(ctx context.Context, id int64) (*NotifySubscription, error) {
	row := oDb.DB.QueryRowContext(ctx, "SELECT "+notifySubscriptionColumns+notifySubscriptionFrom+" WHERE s.id = ?", id)
	s, err := scanNotifySubscription(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

// NotifySubscriptionTaken tells whether another subscription than id sends
// the alerts of the group to the channel.
func (oDb *DB) NotifySubscriptionTaken(ctx context.Context, channelID, groupID, id int64) (bool, error) {
	var n int
	err := oDb.DB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM notification_subscriptions WHERE channel_id = ? AND group_id = ? AND id <> ?",
		channelID, groupID, id).Scan(&n)
	return n > 0, err
}

// InsertNotifySubscription creates a subscription and returns its id.
func (oDb *DB) InsertNotifySubscription(ctx context.Context, s NotifySubscription) (int64, error) {
	filters, err := subscriptionFilterArgs(s)
	if err != nil {
		return 0, err
	}
	args := append([]any{s.ChannelID, s.GroupID, s.Scope, s.MinSeverity, s.DelayMin, s.Enabled}, filters...)
	args = append(args, s.UpdatedBy)
	result, err := oDb.DB.ExecContext(ctx,
		"INSERT INTO notification_subscriptions (channel_id, group_id, scope, min_severity, delay_min, enabled,"+
			" types_include, types_exclude, envs, filterset_id, periods_json, notify_resolved, updated_by, created, updated)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NOW(), NOW())", args...)
	if err != nil {
		return 0, fmt.Errorf("insertNotifySubscription: %w", err)
	}
	oDb.SetChange("notification_subscriptions")
	return result.LastInsertId()
}

// UpdateNotifySubscription writes the subscription s.ID.
func (oDb *DB) UpdateNotifySubscription(ctx context.Context, s NotifySubscription) (bool, error) {
	filters, err := subscriptionFilterArgs(s)
	if err != nil {
		return false, err
	}
	args := append([]any{s.ChannelID, s.GroupID, s.Scope, s.MinSeverity, s.DelayMin, s.Enabled}, filters...)
	args = append(args, s.UpdatedBy, s.ID)
	result, err := oDb.DB.ExecContext(ctx,
		"UPDATE notification_subscriptions SET channel_id = ?, group_id = ?, scope = ?, min_severity = ?, delay_min = ?,"+
			" enabled = ?, types_include = ?, types_exclude = ?, envs = ?, filterset_id = ?, periods_json = ?, notify_resolved = ?,"+
			" updated_by = ?, updated = NOW() WHERE id = ?", args...)
	if err != nil {
		return false, fmt.Errorf("updateNotifySubscription: %w", err)
	}
	oDb.SetChange("notification_subscriptions")
	n, err := result.RowsAffected()
	return n > 0, err
}

// DeleteNotifySubscription deletes a subscription.
func (oDb *DB) DeleteNotifySubscription(ctx context.Context, id int64) (bool, error) {
	result, err := oDb.DB.ExecContext(ctx, "DELETE FROM notification_subscriptions WHERE id = ?", id)
	if err != nil {
		return false, fmt.Errorf("deleteNotifySubscription: %w", err)
	}
	oDb.SetChange("notification_subscriptions")
	n, err := result.RowsAffected()
	return n > 0, err
}

// NotifyActiveSubscriptions returns the subscriptions to serve: enabled, to an
// enabled channel, with the channels they send to, by id.
func (oDb *DB) NotifyActiveSubscriptions(ctx context.Context) ([]NotifySubscription, map[int64]*NotifyChannel, error) {
	rows, err := oDb.DB.QueryContext(ctx, "SELECT "+notifySubscriptionColumns+notifySubscriptionFrom+
		" WHERE s.enabled = 1 AND c.enabled = 1 AND g.id IS NOT NULL ORDER BY s.id")
	if err != nil {
		return nil, nil, fmt.Errorf("notifyActiveSubscriptions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var subs []NotifySubscription
	ids := map[int64]bool{}
	for rows.Next() {
		s, err := scanNotifySubscription(rows.Scan)
		if err != nil {
			return nil, nil, err
		}
		subs = append(subs, *s)
		ids[s.ChannelID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	channels := map[int64]*NotifyChannel{}
	if len(ids) == 0 {
		return subs, channels, nil
	}
	l := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for id := range ids {
		l = append(l, "?")
		args = append(args, id)
	}
	crows, err := oDb.DB.QueryContext(ctx, "SELECT "+notifyChannelColumns+" FROM notification_channels WHERE id IN ("+strings.Join(l, ",")+")", args...)
	if err != nil {
		return nil, nil, fmt.Errorf("notifyActiveSubscriptions: %w", err)
	}
	defer func() { _ = crows.Close() }()
	for crows.Next() {
		c, err := scanNotifyChannel(crows.Scan)
		if err != nil {
			return nil, nil, err
		}
		channels[c.ID] = c
	}
	return subs, channels, crows.Err()
}
