// Package sessioncrypt provides optional AES-256-GCM encryption for session
// files at rest. The key is stored alongside the user config
// (~/.config/reasonix/.session-key, permissions 0o600) and auto-generated on
// first use.
package sessioncrypt

import (
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
// none exists yet. The key file gets 0o600 permissions. If the existing key file
// is corrupted (exists but not exactly 32 bytes), it is backed up before
// generating a new key, and a warning is logged.
func loadOrCreateKey() ([]byte, error) {
	path := KeyPath()
	if path == "" {
		return nil, errors.New("sessioncrypt: cannot resolve user config dir")
	}
	if data, err := os.ReadFile(path); err == nil {
		if len(data) == 32 {
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
	key := make([]byte, 32) // AES-256
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("sessioncrypt: generate key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("sessioncrypt: mkdir: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("sessioncrypt: write key: %w", err)
	}
	return key, nil
}

// Encrypt encrypts plaintext with AES-256-GCM. Output: nonce || ciphertext.
func Encrypt(plaintext []byte) ([]byte, error) {
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
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("sessioncrypt: nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt decrypts data (nonce || ciphertext || tag) with AES-256-GCM.
func Decrypt(data []byte) ([]byte, error) {
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
	if len(data) < nonceSize {
		return nil, errors.New("sessioncrypt: data too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("sessioncrypt: decrypt: %w", err)
	}
	return plaintext, nil
}

// IsEncrypted reports whether data appears to be encrypted (not valid JSON).
func IsEncrypted(data []byte) bool {
	trimmed := data
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\n' || trimmed[0] == '\r') {
		trimmed = trimmed[1:]
	}
	return len(trimmed) == 0 || trimmed[0] != '{'
}
