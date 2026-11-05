package safe

import (
	"crypto/rand"
	"fmt"
)

// secretContentAAD and secretKeyAAD bind a sealed secret and its wrapped key to the label of what holds
// them, "form-connector:12" say, apart from the safe files: a secret copied to
// another record, or over a safe file, does not open.
func secretContentAAD(label string) []byte { return []byte("oc3-secret:v1:content:" + label) }
func secretKeyAAD(label string) []byte     { return []byte("oc3-secret:v1:key:" + label) }

// SealSecret encrypts a secret held in the database, as Seal does a file: a new
// per-record key, wrapped by the active master key. Content is the nonce and the
// ciphertext with its tag, without the file header.
func SealSecret(ring *Keyring, label string, plaintext []byte) (*Sealed, error) {
	master := ring.Active()
	if master == nil {
		return nil, ErrNoKey
	}
	recordKey := make([]byte, KeySize)
	if _, err := rand.Read(recordKey); err != nil {
		return nil, err
	}
	defer wipe(recordKey)
	body, err := seal(recordKey, plaintext, secretContentAAD(label))
	if err != nil {
		return nil, fmt.Errorf("safe: encrypt the secret: %w", err)
	}
	wrapped, err := seal(master.Secret, recordKey, secretKeyAAD(label))
	if err != nil {
		return nil, fmt.Errorf("safe: wrap the secret key: %w", err)
	}
	return &Sealed{Content: body, KeyID: master.ID, WrappedKey: wrapped}, nil
}

// OpenSecret decrypts a secret sealed by SealSecret under the same label.
func OpenSecret(ring *Keyring, label string, content []byte, keyID string, wrappedKey []byte) ([]byte, error) {
	master := ring.Get(keyID)
	if master == nil {
		return nil, fmt.Errorf("safe: the master key %q of the secret is not in the keyring", keyID)
	}
	recordKey, err := open(master.Secret, wrappedKey, secretKeyAAD(label))
	if err != nil {
		return nil, err
	}
	defer wipe(recordKey)
	return open(recordKey, content, secretContentAAD(label))
}

// RewrapSecret wraps the per-record key of a secret sealed by SealSecret with
// the active master key, the content left as it is: what a rotation of the
// master key changes.
func RewrapSecret(ring *Keyring, label, keyID string, wrappedKey []byte) (string, []byte, error) {
	master := ring.Get(keyID)
	if master == nil {
		return "", nil, fmt.Errorf("safe: the master key %q of the secret is not in the keyring", keyID)
	}
	active := ring.Active()
	if active == nil {
		return "", nil, ErrNoKey
	}
	recordKey, err := open(master.Secret, wrappedKey, secretKeyAAD(label))
	if err != nil {
		return "", nil, err
	}
	defer wipe(recordKey)
	wrapped, err := seal(active.Secret, recordKey, secretKeyAAD(label))
	if err != nil {
		return "", nil, err
	}
	return active.ID, wrapped, nil
}
