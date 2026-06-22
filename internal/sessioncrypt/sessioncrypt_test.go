package sessioncrypt

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// setTempConfigHome sets XDG_CONFIG_HOME to a temp dir so KeyPath() points
// inside that dir. It returns the previous value for restoration.
func setTempConfigHome(t *testing.T) string {
	t.Helper()
	old := os.Getenv("XDG_CONFIG_HOME")
	td := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", td)
	return old
}

// TestKeyGeneratedOnFirstUse verifies that loadOrCreateKey creates a key file
// when none exists, that the key is 32 bytes, and that the permissions are 0o600.
func TestKeyGeneratedOnFirstUse(t *testing.T) {
	setTempConfigHome(t)

	keyPath := KeyPath()
	if keyPath == "" {
		t.Fatal("KeyPath() returned empty — user config dir unavailable")
	}

	// No key file exists yet. Calling Encrypt (which calls loadOrCreateKey)
	// should create one.
	data, err := Encrypt([]byte("hello"))
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Key file must now exist.
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file not created: %v", err)
	}

	// Verify permissions are exactly 0o600.
	const wantMode os.FileMode = 0o600
	gotMode := info.Mode().Perm()
	if gotMode != wantMode {
		t.Errorf("key file permissions: got %04o, want %04o", gotMode, wantMode)
	}

	// Verify key content length.
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("reading key file: %v", err)
	}
	if len(key) != keyLen {
		t.Errorf("key length: got %d, want %d", len(key), keyLen)
	}

	// Decrypt must succeed with the key that was just created.
	plain, err := Decrypt(data)
	if err != nil {
		t.Fatalf("Decrypt failed with newly created key: %v", err)
	}
	if string(plain) != "hello" {
		t.Errorf("Decrypt plaintext: got %q, want %q", string(plain), "hello")
	}
}

// TestKeyLoadingExisting verifies that a pre-existing valid key file is loaded
// and used correctly.
func TestKeyLoadingExisting(t *testing.T) {
	setTempConfigHome(t)

	// Create the key directory and write a 32-byte key.
	keyPath := KeyPath()
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	wantKey := make([]byte, keyLen)
	for i := range wantKey {
		wantKey[i] = byte(i)
	}
	if err := os.WriteFile(keyPath, wantKey, 0o600); err != nil {
		t.Fatal(err)
	}

	// Encrypt should load the existing key, not create a new one.
	data, err := Encrypt([]byte("existing key test"))
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	plain, err := Decrypt(data)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if string(plain) != "existing key test" {
		t.Errorf("plaintext mismatch: got %q", string(plain))
	}

	// Verify the key file was NOT overwritten (key bytes unchanged).
	gotKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotKey, wantKey) {
		t.Error("key file was overwritten on load")
	}
}

// TestEncryptDecryptRoundtrip exercises Encrypt/Decrypt with various
// plaintext sizes and ensures correct roundtrip.
func TestEncryptDecryptRoundtrip(t *testing.T) {
	setTempConfigHome(t)

	tests := []struct {
		name      string
		plaintext []byte
	}{
		{"empty", []byte{}},
		{"single byte", []byte{0x42}},
		{"short", []byte("hello, world")},
		{"long (multiple blocks)", bytes.Repeat([]byte("A"), 1000)},
		{"binary", []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}},
		{"unicode", []byte("Привет, мир! 🎉")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, err := Encrypt(tt.plaintext)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}
			// Must be larger than plaintext (due to nonce + tag + magic)
			if len(enc) <= len(tt.plaintext) {
				t.Error("encrypted output too short")
			}
			// Must start with magic prefix
			if !IsEncrypted(enc) {
				t.Error("IsEncrypted returned false on encrypted data")
			}
			// Decrypt
			dec, err := Decrypt(enc)
			if err != nil {
				t.Fatalf("Decrypt: %v", err)
			}
			if !bytes.Equal(dec, tt.plaintext) {
				t.Errorf("plaintext mismatch:\ngot  %x\nwant %x", dec, tt.plaintext)
			}
		})
	}
}

// TestEncryptDecryptWithAAD verifies EncryptWithAAD / DecryptWithAAD works
// and that AAD mismatch is caught.
func TestEncryptDecryptWithAAD(t *testing.T) {
	setTempConfigHome(t)

	aad := []byte("my-aad-data")
	plain := []byte("secret with context")

	enc, err := encryptWithAAD(plain, aad)
	if err != nil {
		t.Fatalf("encryptWithAAD: %v", err)
	}
	if !IsEncrypted(enc) {
		t.Error("IsEncrypted false on AAD-encrypted data")
	}
	// Decrypt with same AAD must succeed.
	dec, err := DecryptWithAAD(enc, aad)
	if err != nil {
		t.Fatalf("DecryptWithAAD with correct AAD: %v", err)
	}
	if !bytes.Equal(dec, plain) {
		t.Errorf("plaintext mismatch: got %q, want %q", string(dec), string(plain))
	}
	// Decrypt with wrong AAD must fail.
	_, err = DecryptWithAAD(enc, []byte("wrong-aad"))
	if err == nil {
		t.Fatal("DecryptWithAAD with wrong AAD should have failed")
	}
	// Decrypt with nil AAD (used by Encrypt) must fail when data was encrypted with AAD.
	_, err = Decrypt(enc)
	if err == nil {
		t.Fatal("Decrypt (nil AAD) on AAD-encrypted data should have failed")
	}
}

// TestErrorCorruptedCiphertext checks that various malformed inputs produce
// appropriate errors.
func TestErrorCorruptedCiphertext(t *testing.T) {
	setTempConfigHome(t)

	// First, get a valid encrypted blob so we know the key exists.
	valid, err := Encrypt([]byte("valid"))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		data []byte
	}{
		{"nil data", nil},
		{"empty data", []byte{}},
		{"too short", []byte{0x00, 0x01, 0x02}},
		{"wrong magic", []byte("{\"hello\"}")},
		{"truncated-magic-only", valid[:3]},
		{"truncated-no-nonce", valid[:5]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decrypt(tt.data)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}

	// Corrupted ciphertext: flip a byte in the ciphertext portion.
	if len(valid) > 20 {
		corrupted := make([]byte, len(valid))
		copy(corrupted, valid)
		corrupted[len(corrupted)-1] ^= 0xFF // flip last byte (tag area)
		_, err := Decrypt(corrupted)
		if err == nil {
			t.Error("expected error for corrupted ciphertext, got nil")
		}
	}
}

// TestDecryptWithWrongKey verifies that decrypting with a different key (e.g.
// after the key file is replaced) yields an error.
func TestDecryptWithWrongKey(t *testing.T) {
	setTempConfigHome(t)

	// Encrypt with the first key.
	plain := []byte("this will be lost")
	enc, err := Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}

	// Remove the existing key and create a different one.
	keyPath := KeyPath()
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	newKey := make([]byte, keyLen)
	for i := range newKey {
		newKey[i] = 0xAB
	}
	if err := os.WriteFile(keyPath, newKey, 0o600); err != nil {
		t.Fatal(err)
	}

	// Decrypt should fail because the key doesn't match.
	_, err = Decrypt(enc)
	if err == nil {
		t.Error("expected error when decrypting with wrong key, got nil")
	}
}

// TestIsEncrypted verifies the magic-prefix detection logic.
func TestIsEncrypted(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"nil", nil, false},
		{"empty", []byte{}, false},
		{"json-starts-with-brace", []byte("{"), false},
		{"json-like", []byte("{\"session\": \"data\"}"), false},
		{"only-magic", []byte{0x1d, 0x73, 0x63, 0x72}, true},
		{"magic-plus-data", append([]byte{0x1d, 0x73, 0x63, 0x72}, []byte("more")...), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsEncrypted(tt.data)
			if got != tt.want {
				t.Errorf("IsEncrypted(%q) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

// TestKeyFilePermissionsEnforcedOnCreate verifies that after creating the key
// file, its permissions are exactly 0o600 regardless of umask.
func TestKeyFilePermissionsEnforcedOnCreate(t *testing.T) {
	setTempConfigHome(t)

	// Call Encrypt to trigger key creation.
	if _, err := Encrypt([]byte("perms")); err != nil {
		t.Fatal(err)
	}

	keyPath := KeyPath()
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	got := info.Mode().Perm()
	if got != 0o600 {
		t.Errorf("key file permissions: got %04o, want 0600", got)
	}
}

// TestCorruptedKeyFileIsBackedUp simulates a key file that exists but has the
// wrong length (which loadOrCreateKey treats as corrupted).
func TestCorruptedKeyFileIsBackedUp(t *testing.T) {
	setTempConfigHome(t)

	keyPath := KeyPath()
	// Create directory and write a corrupt (short) key file.
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	corruptKey := []byte("too-short")
	if err := os.WriteFile(keyPath, corruptKey, 0o600); err != nil {
		t.Fatal(err)
	}

	// Encrypt should generate a new key and back up the corrupt one.
	if _, err := Encrypt([]byte("after corruption")); err != nil {
		t.Fatalf("Encrypt after corrupt key: %v", err)
	}

	// The key file should now contain a valid 32-byte key.
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != keyLen {
		t.Errorf("new key length: got %d, want %d", len(key), keyLen)
	}

	// A backup file should exist.
	matches, err := filepath.Glob(keyPath + ".corrupt.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Error("expected a backup file for the corrupt key, none found")
	}

	// Encrypt/Decrypt should work with the new key.
	data, err := Encrypt([]byte("works with new key"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Decrypt(data)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "works with new key" {
		t.Errorf("plaintext: got %q", string(plain))
	}
}
