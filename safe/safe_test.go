package safe

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(fill byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, KeySize))
}

func testRing(t *testing.T, yaml string) *Keyring {
	t.Helper()
	ring, err := ParseKeyring([]byte(yaml))
	if err != nil {
		t.Fatalf("parse keyring: %s", err)
	}
	return ring
}

func TestParseKeyring(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"no key", "keys: []", "no master key"},
		{"no active key", "keys:\n- id: a\n  key: " + testKey(1), "no key is active"},
		{"two active keys", "keys:\n- id: a\n  key: " + testKey(1) + "\n  active: true\n- id: b\n  key: " + testKey(2) + "\n  active: true", "both active"},
		{"short key", "keys:\n- id: a\n  key: " + base64.StdEncoding.EncodeToString([]byte("short")) + "\n  active: true", "base64 of 32 bytes"},
		{"duplicate id", "keys:\n- id: a\n  key: " + testKey(1) + "\n  active: true\n- id: a\n  key: " + testKey(2), "listed twice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseKeyring([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error with %q", err, tc.want)
			}
			if strings.Contains(err.Error(), testKey(1)) {
				t.Fatalf("the error quotes a key: %s", err)
			}
		})
	}
}

func TestLoadKeyringRefusesReadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.yaml")
	content := []byte("keys:\n- id: a\n  key: " + testKey(1) + "\n  active: true\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyring(path); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("a keys file readable by others must be refused, got %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyring(path); err != nil {
		t.Fatalf("load: %s", err)
	}
}

func TestSealOpen(t *testing.T) {
	ring := testRing(t, "keys:\n- id: k1\n  key: "+testKey(1)+"\n  active: true\n")
	name := "safe.uuid.0123456789abcdef.6b6579.pem"
	plaintext := []byte("-----BEGIN PRIVATE KEY-----\nsecret\n")

	sealed, err := Seal(ring, name, plaintext)
	if err != nil {
		t.Fatalf("seal: %s", err)
	}
	if bytes.Contains(sealed.Content, []byte("secret")) {
		t.Fatal("the plaintext shows in the sealed content")
	}
	if !IsSealed(sealed.Content) || IsSealed(plaintext) {
		t.Fatal("IsSealed tells the formats apart wrongly")
	}
	got, err := Open(ring, name, sealed.Content, sealed.KeyID, sealed.WrappedKey)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("open: %v %q", err, got)
	}

	t.Run("altered content", func(t *testing.T) {
		altered := bytes.Clone(sealed.Content)
		altered[len(altered)-1] ^= 1
		if _, err := Open(ring, name, altered, sealed.KeyID, sealed.WrappedKey); !errors.Is(err, ErrOpen) {
			t.Fatalf("got %v, want ErrOpen", err)
		}
	})
	t.Run("content of another file", func(t *testing.T) {
		other := "safe.uuid.fedcba9876543210.6b6579.pem"
		if _, err := Open(ring, other, sealed.Content, sealed.KeyID, sealed.WrappedKey); !errors.Is(err, ErrOpen) {
			t.Fatalf("got %v, want ErrOpen", err)
		}
	})
	t.Run("another master key", func(t *testing.T) {
		wrong := testRing(t, "keys:\n- id: k1\n  key: "+testKey(9)+"\n  active: true\n")
		if _, err := Open(wrong, name, sealed.Content, sealed.KeyID, sealed.WrappedKey); !errors.Is(err, ErrOpen) {
			t.Fatalf("got %v, want ErrOpen", err)
		}
	})
	t.Run("retired master key", func(t *testing.T) {
		other := testRing(t, "keys:\n- id: k2\n  key: "+testKey(2)+"\n  active: true\n")
		if _, err := Open(other, name, sealed.Content, sealed.KeyID, sealed.WrappedKey); err == nil || !strings.Contains(err.Error(), `"k1"`) {
			t.Fatalf("got %v, want a missing key error", err)
		}
	})
	t.Run("no key", func(t *testing.T) {
		if _, err := Seal(nil, name, plaintext); !errors.Is(err, ErrNoKey) {
			t.Fatalf("got %v, want ErrNoKey", err)
		}
	})
}

func TestRewrap(t *testing.T) {
	old := testRing(t, "keys:\n- id: k1\n  key: "+testKey(1)+"\n  active: true\n")
	name := "safe.uuid.0123456789abcdef.txt"
	sealed, err := Seal(old, name, []byte("content"))
	if err != nil {
		t.Fatal(err)
	}
	rotated := testRing(t, "keys:\n- id: k1\n  key: "+testKey(1)+"\n- id: k2\n  key: "+testKey(2)+"\n  active: true\n")
	keyID, wrapped, err := Rewrap(rotated, name, sealed.KeyID, sealed.WrappedKey)
	if err != nil || keyID != "k2" {
		t.Fatalf("rewrap: %v %s", err, keyID)
	}
	// Once rewrapped, the old key is no longer needed.
	newOnly := testRing(t, "keys:\n- id: k2\n  key: "+testKey(2)+"\n  active: true\n")
	got, err := Open(newOnly, name, sealed.Content, keyID, wrapped)
	if err != nil || string(got) != "content" {
		t.Fatalf("open after rewrap: %v %q", err, got)
	}
}

func TestNames(t *testing.T) {
	name, err := NewName("resolv.conf")
	if err != nil {
		t.Fatal(err)
	}
	if !regexpName.MatchString(name) || !strings.HasSuffix(name, ".conf") {
		t.Fatalf("new name %q is not a web2py upload name", name)
	}
	if got := OriginalName(name); got != "resolv.conf" {
		t.Fatalf("original name: got %q", got)
	}
	long, err := NewName(strings.Repeat("x", 400) + ".pem")
	if err != nil || len(long) > nameColumnSize || !strings.HasSuffix(long, ".pem") {
		t.Fatalf("long name %d %q: %v", len(long), long[len(long)-8:], err)
	}

	store := Store{Dir: "/srv/safe"}
	path, err := store.Path("safe.uuid.a1b2c3d4e5f60718.7265736f6c762e636f6e66.conf")
	if err != nil || path != "/srv/safe/safe.uuid/a1/safe.uuid.a1b2c3d4e5f60718.7265736f6c762e636f6e66.conf" {
		t.Fatalf("path: %q %v", path, err)
	}
	for _, bad := range []string{"../etc/passwd", "safe.uuid.a1/../x.txt", "safe.uuid.zz.txt", "", "safe.uuid.a1b2.txt/x"} {
		if _, err := store.Path(bad); !errors.Is(err, ErrName) {
			t.Errorf("%q: got %v, want ErrName", bad, err)
		}
	}
}

func TestStoreWrite(t *testing.T) {
	store := Store{Dir: t.TempDir(), FileMode: 0o640}
	name := "safe.uuid.a1b2c3d4e5f60718.txt"
	if err := store.Write(name, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(name, []byte("v2")); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read(name)
	if err != nil || string(got) != "v2" {
		t.Fatalf("read: %v %q", err, got)
	}
	path, _ := store.Path(name)
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %04o", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("a temporary file was left behind: %v", entries)
	}
	if err := store.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(name); err != nil {
		t.Fatalf("removing a missing file: %v", err)
	}
}

func TestSecretRoundTripAndBinding(t *testing.T) {
	ring := testRing(t, "keys:\n- id: k1\n  key: "+testKey(1)+"\n  active: true\n")
	sealed, err := SealSecret(ring, "form-connector:1", []byte(`{"token":"s3cr3t"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenSecret(ring, "form-connector:1", sealed.Content, sealed.KeyID, sealed.WrappedKey)
	if err != nil || string(got) != `{"token":"s3cr3t"}` {
		t.Fatalf("round trip: %q %v", got, err)
	}
	// Moved to another record: refused.
	if _, err := OpenSecret(ring, "form-connector:2", sealed.Content, sealed.KeyID, sealed.WrappedKey); err == nil {
		t.Error("a secret opened under another label")
	}
	// A safe file's label never opens it.
	if _, err := Open(ring, "form-connector:1", sealed.Content, sealed.KeyID, sealed.WrappedKey); err == nil {
		t.Error("a secret opened as a safe file")
	}
	if _, err := SealSecret(nil, "x", []byte("y")); err != ErrNoKey {
		t.Errorf("no keyring: %v, want ErrNoKey", err)
	}
}

func TestRewrapSecret(t *testing.T) {
	key := func(b byte) string {
		k := make([]byte, KeySize)
		for i := range k {
			k[i] = b
		}
		return base64.StdEncoding.EncodeToString(k)
	}
	old, err := ParseKeyring([]byte("keys:\n  - id: a\n    key: " + key(1) + "\n    active: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := SealSecret(old, "notification-channel:3", []byte(`{"url":"https://x"}`))
	if err != nil {
		t.Fatal(err)
	}
	ring, err := ParseKeyring([]byte("keys:\n  - id: a\n    key: " + key(1) + "\n  - id: b\n    key: " + key(2) + "\n    active: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := RewrapSecret(ring, "notification-channel:4", sealed.KeyID, sealed.WrappedKey); err == nil {
		t.Error("rewrapped under another label")
	}
	keyID, wrapped, err := RewrapSecret(ring, "notification-channel:3", sealed.KeyID, sealed.WrappedKey)
	if err != nil || keyID != "b" {
		t.Fatalf("rewrap: %s %v", keyID, err)
	}
	only, err := ParseKeyring([]byte("keys:\n  - id: b\n    key: " + key(2) + "\n    active: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := OpenSecret(only, "notification-channel:3", sealed.Content, keyID, wrapped)
	if err != nil || string(plain) != `{"url":"https://x"}` {
		t.Fatalf("open after rewrap: %q %v", plain, err)
	}
}
