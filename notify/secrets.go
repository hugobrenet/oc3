package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/safe"
)

// The secrets of a channel, sealed together as one JSON object.
const (
	SecretURL    = "url"
	SecretSigner = "secret"
)

// ChannelLabel is the label the secrets of a channel are sealed under: copied
// to another channel, they do not open.
func ChannelLabel(id int64) string {
	return "notification-channel:" + strconv.FormatInt(id, 10)
}

// ErrNoKey tells a channel with secrets cannot be read or written without the
// safe master key.
var ErrNoKey = errors.New("the secrets of the notification channels need the safe master key: configure safe.keys_file")

// OpenSecrets returns the secrets of a channel, none when it has none.
func OpenSecrets(ring *safe.Keyring, c *cdb.NotifyChannel) (map[string]string, error) {
	out := map[string]string{}
	if c.Secrets == nil {
		return out, nil
	}
	if ring == nil {
		return nil, ErrNoKey
	}
	plain, err := safe.OpenSecret(ring, ChannelLabel(c.ID), c.Secrets.Content, c.Secrets.KeyID, c.Secrets.WrappedKey)
	if err != nil {
		return nil, fmt.Errorf("the secrets of channel %s do not open: %w", c.Name, err)
	}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("the secrets of channel %s are unreadable", c.Name)
	}
	return out, nil
}

// SealSecrets seals the secrets of the channel id, nil when there are none.
func SealSecrets(ring *safe.Keyring, id int64, secrets map[string]string) (*cdb.SealedSecret, error) {
	if len(secrets) == 0 {
		return nil, nil
	}
	if ring == nil || ring.Active() == nil {
		return nil, ErrNoKey
	}
	plain, err := json.Marshal(secrets)
	if err != nil {
		return nil, err
	}
	sealed, err := safe.SealSecret(ring, ChannelLabel(id), plain)
	if err != nil {
		return nil, err
	}
	return &cdb.SealedSecret{Content: sealed.Content, KeyID: sealed.KeyID, WrappedKey: sealed.WrappedKey}, nil
}

// DestinationOf returns the channel as the notifications send to it.
func DestinationOf(ring *safe.Keyring, c *cdb.NotifyChannel) (Destination, error) {
	dest := Destination{
		Type:      c.Type,
		Name:      c.Name,
		Addresses: c.Settings.Addresses,
		Username:  c.Settings.Username,
		IconURL:   c.Settings.IconURL,
	}
	secrets, err := OpenSecrets(ring, c)
	if err != nil {
		return dest, err
	}
	dest.URL = secrets[SecretURL]
	dest.Secret = secrets[SecretSigner]
	return dest, nil
}
