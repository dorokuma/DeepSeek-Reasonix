// Package sessioncrypt provides optional AES-256-GCM encryption for session
// files at rest. The key is stored alongside the user config
// (~/.config/reasonix/.session-key, permissions 0o600) and auto-generated on
// first use.
//
// Encrypted data layout: magic (4 bytes) || nonce (12 bytes) || ciphertext || tag.
// The magic prefix (0x1d 0x73 0x63 0x72) reliably distinguishes encrypted data
// from JSON plaintext (which starts with '{').
package sessioncrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// magicPrefix marks encrypted output so IsEncrypted can distinguish it from
// JSON plaintext without false positives.
var magicPrefix = []byte{0x1d, 0x73, 0x63, 0x72} // non-printable + "scr"

const (
	magicLen = 4
	keyLen   = 32 // AES-256
)

// KeyPath returns the path to the session key file.
func KeyPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "reasonix", ".session-key")
}

// Enabled reports whether the key directory is resolvable.
func Enabled() bool { return KeyPath() != "" }

// loadOrCreateKey loads the session key from disk, creating a new random key if
// none exists yet. It uses O_EXCL to avoid a TOCTOU race when two processes
// create the key simultaneously. Each write re-enforces 0o600 permissions. If
// the existing key file is corrupted (exists but not exactly 32 bytes), it is
// backed up before generating a new key, and a warning is logged.
func loadOrCreateKey() ([]byte, error) {
	path := KeyPath()
	if path == "" {
		return nil, errors.New("sessioncrypt: cannot resolve user config dir")
	}
	if data, err := os.ReadFile(path); err == nil {
		if len(data) == keyLen {
			return data, nil
		}
		// File exists but length != 32 — corrupted. Back it up and warn.
		backup := path + ".corrupt." + strconv.FormatInt(time.Now().UnixMilli(), 36)
		if renameErr := os.Rename(path, backup); renameErr != nil {
			slog.Warn("sessioncrypt: key file corrupted, backup failed", "path", path, "backup", backup, "err", renameErr)
		} else {
			slog.Warn("sessioncrypt: key file corrupted, backed up", "path", path, "backup", backup)
		}
	} else if !os.IsNotExist(err) {
		slog.Warn("sessioncrypt: reading key file", "path", path, "err", err)
	}
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("sessioncrypt: generate key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("sessioncrypt: mkdir: %w", err)
	}
	// Atomic create with O_EXCL to prevent TOCTOU race between our ReadFile
	// check and this write. If another process created the file first, read it.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		if _, werr := f.Write(key); werr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("sessioncrypt: write key: %w", werr)
		}
		if cerr := f.Close(); cerr != nil {
			return nil, fmt.Errorf("sessioncrypt: close key: %w", cerr)
		}
	} else if os.IsExist(err) {
		// Another process created the file between our ReadFile and OpenFile.
		if data, rerr := os.ReadFile(path); rerr == nil && len(data) == keyLen {
			return data, nil
		}
		return nil, fmt.Errorf("sessioncrypt: key file raced and unreadable: %w", err)
	} else {
		return nil, fmt.Errorf("sessioncrypt: create key file: %w", err)
	}
	// Re-enforce permissions after write (defense-in-depth: umask or pre-existing
	// file with wrong mode could leave the key readable).
	if err := os.Chmod(path, 0o600); err != nil {
		slog.Warn("sessioncrypt: chmod key file", "path", path, "err", err)
	}
	return key, nil
}

// encryptWithAAD encrypts plaintext with AES-256-GCM, binding aad (additional
// authenticated data) to the ciphertext. Output: magic(4) || nonce(12) || ciphertext || tag.
func encryptWithAAD(plaintext, aad []byte) ([]byte, error) {
	key, err := loadOrCreateKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("sessioncrypt: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("sessioncrypt: gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("sessioncrypt: nonce: %w", err)
	}
	// Layout: magic || nonce || ciphertext(with tag appended by Seal)
	out := make([]byte, magicLen+nonceSize+len(plaintext)+gcm.Overhead())
	copy(out, magicPrefix)
	copy(out[magicLen:], nonce)
	gcm.Seal(out[magicLen+nonceSize:magicLen+nonceSize], nonce, plaintext, aad)
	return out, nil
}

// DecryptWithAAD decrypts data (magic || nonce || ciphertext || tag) and
// verifies the AAD.
func DecryptWithAAD(data, aad []byte) ([]byte, error) {
	if len(data) < magicLen {
		return nil, errors.New("sessioncrypt: data too short (missing magic)")
	}
	if !bytes.HasPrefix(data, magicPrefix) {
		return nil, errors.New("sessioncrypt: invalid magic prefix — data is not encrypted or is corrupted")
	}
	key, err := loadOrCreateKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("sessioncrypt: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("sessioncrypt: gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	payload := data[magicLen:]
	if len(payload) < nonceSize+gcm.Overhead() {
		return nil, fmt.Errorf("sessioncrypt: data too short: need at least nonce(%d)+tag(%d), got %d",
			nonceSize, gcm.Overhead(), len(payload))
	}
	nonce, ciphertext := payload[:nonceSize], payload[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("sessioncrypt: decrypt: %w", err)
	}
	return plaintext, nil
}

// Encrypt encrypts plaintext with AES-256-GCM.
// Output: magic(4) || nonce(12) || ciphertext || tag.
func Encrypt(plaintext []byte) ([]byte, error) {
	return encryptWithAAD(plaintext, nil)
}

// Decrypt decrypts data (magic || nonce || ciphertext || tag) produced by Encrypt.
func Decrypt(data []byte) ([]byte, error) {
	return DecryptWithAAD(data, nil)
}

// IsEncrypted reports whether data appears to be encrypted (starts with the
// magic prefix), as opposed to plaintext JSON (starts with '{'). Unlike the
// old heuristic that only checked for '{', this uses a reliable 4-byte magic.
func IsEncrypted(data []byte) bool {
	return bytes.HasPrefix(data, magicPrefix)
}
