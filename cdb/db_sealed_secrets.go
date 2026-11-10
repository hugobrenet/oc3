package cdb

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SealedSecretTable is a table whose rows may hold secrets sealed with the safe
// master key (safe.SealSecret), apart from the safe files: one JSON object per
// row, in its secrets, secrets_key_id and secrets_wrapped_key columns, sealed
// under the label LabelPrefix and the id of the row.
type SealedSecretTable struct {
	Table       string
	Kind        string
	LabelPrefix string
}

// SealedSecretTables are the tables holding sealed secrets: a rotation of the
// master key rewraps them with the safe files, and the safe view lists them.
var SealedSecretTables = []SealedSecretTable{
	{Table: "form_connectors", Kind: "form_connector", LabelPrefix: "form-connector:"},
	{Table: "notification_channels", Kind: "notification_channel", LabelPrefix: "notification-channel:"},
}

// Label returns the label the secrets of the row id are sealed under.
func (t SealedSecretTable) Label(id int64) string {
	return t.LabelPrefix + strconv.FormatInt(id, 10)
}

// SealedSecretRef is a row holding sealed secrets.
type SealedSecretRef struct {
	Table      SealedSecretTable
	ID         int64
	Name       string
	KeyID      string
	WrappedKey []byte
	Updated    time.Time
}

func (oDb *DB) sealedSecrets(ctx context.Context, t SealedSecretTable, where string, args ...any) ([]SealedSecretRef, error) {
	query := "SELECT id, name, secrets_key_id, secrets_wrapped_key, updated FROM " + t.Table +
		" WHERE secrets IS NOT NULL AND secrets_key_id IS NOT NULL"
	if where != "" {
		query += " AND " + where
	}
	rows, err := oDb.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sealedSecrets %s: %w", t.Table, err)
	}
	defer func() { _ = rows.Close() }()
	var out []SealedSecretRef
	for rows.Next() {
		r := SealedSecretRef{Table: t}
		if err := rows.Scan(&r.ID, &r.Name, &r.KeyID, &r.WrappedKey, &r.Updated); err != nil {
			return nil, fmt.Errorf("sealedSecrets %s: %w", t.Table, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SealedSecrets returns the rows holding sealed secrets of the kinds given,
// every kind when none, by kind and name.
func (oDb *DB) SealedSecrets(ctx context.Context, kinds ...string) ([]SealedSecretRef, error) {
	var out []SealedSecretRef
	for _, t := range SealedSecretTables {
		if len(kinds) > 0 && !containsString(kinds, t.Kind) {
			continue
		}
		l, err := oDb.sealedSecrets(ctx, t, "1=1 ORDER BY name")
		if err != nil {
			return nil, err
		}
		out = append(out, l...)
	}
	return out, nil
}

// SealedSecretsToRewrap returns the rows whose secrets are wrapped with
// another master key than activeKeyID, at most limit per table.
func (oDb *DB) SealedSecretsToRewrap(ctx context.Context, activeKeyID string, limit int) ([]SealedSecretRef, error) {
	var out []SealedSecretRef
	for _, t := range SealedSecretTables {
		l, err := oDb.sealedSecrets(ctx, t, "secrets_key_id <> ? ORDER BY id LIMIT ?", activeKeyID, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, l...)
	}
	return out, nil
}

// SetSealedSecretKey records the key of the secrets of a row rewrapped, if the
// row still has the key it was read with: false when it changed meanwhile.
func (oDb *DB) SetSealedSecretKey(ctx context.Context, r SealedSecretRef, keyID string, wrapped []byte) (bool, error) {
	result, err := oDb.DB.ExecContext(ctx, "UPDATE "+r.Table.Table+
		" SET secrets_key_id = ?, secrets_wrapped_key = ? WHERE id = ? AND secrets_key_id = ?",
		keyID, wrapped, r.ID, r.KeyID)
	if err != nil {
		return false, fmt.Errorf("setSealedSecretKey %s %d: %w", r.Table.Table, r.ID, err)
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

func containsString(l []string, s string) bool {
	for _, x := range l {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
