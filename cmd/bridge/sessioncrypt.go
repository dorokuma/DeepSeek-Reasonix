package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

var magicPrefix = []byte{0x1d, 0x73, 0x63, 0x72} // non-printable + "scr"

const (
	magicLen = 4
	keyLen   = 32 // AES-256
)

// cryptCache holds the cached session key and its GCM block, protected by a mutex.
var cryptCache = struct {
	mu  sync.Mutex
	key []byte
	gcm cipher.AEAD
}{}

func KeyPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "reasonix", ".session-key")
}

func loadOrCreateKey() ([]byte, error) {
	// Fast path: return cached key.
	cryptCache.mu.Lock()
	if cryptCache.key != nil {
		k := cryptCache.key
		cryptCache.mu.Unlock()
		return k, nil
	}
	cryptCache.mu.Unlock()

	path := KeyPath()
	if path == "" {
		return nil, errors.New("sessioncrypt: cannot resolve user config dir")
	}
	if data, err := os.ReadFile(path); err == nil {
		if len(data) == keyLen {
			cryptCache.mu.Lock()
			cryptCache.key = data
			cryptCache.gcm = nil // force GCM rebuild on next use
			cryptCache.mu.Unlock()
			return data, nil
		}
		backup := path + ".corrupt." + strconv.FormatInt(time.Now().UnixMilli(), 36)
		_ = os.Rename(path, backup)
	}
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("sessioncrypt: generate key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("sessioncrypt: mkdir: %w", err)
	}
	// Create temp file, write key, set permissions, then rename atomically.
	// This avoids the Chmod race window on the final path.
	tmpf, err := os.CreateTemp(filepath.Dir(path), ".session-key-*")
	if err != nil {
		return nil, fmt.Errorf("sessioncrypt: create temp file: %w", err)
	}
	tmpPath := tmpf.Name()
	cleanup := true
	defer func() {
		if cleanup {
			if err := os.Remove(tmpPath); err != nil {
				log.Printf("sessioncrypt: remove temp %s: %v", tmpPath, err)
			}
		}
	}()
	if _, err := tmpf.Write(key); err != nil {
		tmpf.Close()
		return nil, fmt.Errorf("sessioncrypt: write key: %w", err)
	}
	if err := tmpf.Chmod(0o600); err != nil {
		tmpf.Close()
		return nil, fmt.Errorf("sessioncrypt: chmod temp key: %w", err)
	}
	if err := tmpf.Close(); err != nil {
		return nil, fmt.Errorf("sessioncrypt: close temp key: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return nil, fmt.Errorf("sessioncrypt: rename key: %w", err)
	}
	cleanup = false // keep the file at its final path
	cryptCache.mu.Lock()
	cryptCache.key = key
	cryptCache.gcm = nil
	cryptCache.mu.Unlock()
	return key, nil
}

// getOrCreateGCM returns the cached GCM or builds a new one from the cached key.
func getOrCreateGCM() (cipher.AEAD, error) {
	cryptCache.mu.Lock()
	defer cryptCache.mu.Unlock()
	if cryptCache.gcm != nil {
		return cryptCache.gcm, nil
	}
	if cryptCache.key == nil {
		return nil, errors.New("sessioncrypt: no key loaded")
	}
	block, err := aes.NewCipher(cryptCache.key)
	if err != nil {
		return nil, fmt.Errorf("sessioncrypt: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("sessioncrypt: gcm: %w", err)
	}
	cryptCache.gcm = gcm
	return gcm, nil
}

// encryptWithAAD encrypts plaintext with AES-256-GCM, binding aad (additional
// authenticated data) to the ciphertext. Output: magic(4) || nonce(12) || ciphertext || tag.
func encryptWithAAD(plaintext, aad []byte) ([]byte, error) {
	_, err := loadOrCreateKey()
	if err != nil {
		return nil, err
	}
	gcm, err := getOrCreateGCM()
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("sessioncrypt: nonce: %w", err)
	}
	// Layout: magic || nonce || ciphertext(with tag appended by Seal)
	out := make([]byte, magicLen+nonceSize+len(plaintext)+gcm.Overhead())
	copy(out, magicPrefix)
	copy(out[magicLen:], nonce)
	gcm.Seal(out[magicLen+nonceSize:magicLen+nonceSize], nonce, plaintext, aad)
	return out, nil
}

// Encrypt encrypts plaintext with AES-256-GCM.
// Output: magic(4) || nonce(12) || ciphertext || tag.
func Encrypt(plaintext []byte) ([]byte, error) {
	return encryptWithAAD(plaintext, nil)
}

func DecryptWithAAD(data, aad []byte) ([]byte, error) {
	if len(data) < magicLen {
		return nil, errors.New("sessioncrypt: data too short (missing magic)")
	}
	if !bytes.HasPrefix(data, magicPrefix) {
		return nil, errors.New("sessioncrypt: invalid magic prefix")
	}
	_, err := loadOrCreateKey()
	if err != nil {
		return nil, err
	}
	gcm, err := getOrCreateGCM()
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	payload := data[magicLen:]
	if len(payload) < nonceSize+gcm.Overhead() {
		return nil, fmt.Errorf("sessioncrypt: data too short")
	}
	nonce, ciphertext := payload[:nonceSize], payload[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("sessioncrypt: decrypt: %w", err)
	}
	return plaintext, nil
}

func Decrypt(data []byte) ([]byte, error) {
	return DecryptWithAAD(data, nil)
}

// RotateSessionKey generates a new random key, writes it to the key file
// atomically (temp file + rename), and updates the in-memory cache.
func RotateSessionKey() error {
	path := KeyPath()
	if path == "" {
		return errors.New("sessioncrypt: cannot resolve user config dir")
	}
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("generate new session key: %w", err)
	}

	// Atomic write: temp file in same directory, then rename.
	tmpf, err := os.CreateTemp(filepath.Dir(path), ".session-key-*")
	if err != nil {
		return fmt.Errorf("create temp key file: %w", err)
	}
	tmpPath := tmpf.Name()
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpPath)
		}
	}()
	if _, err := tmpf.Write(key); err != nil {
		tmpf.Close()
		return fmt.Errorf("write temp key: %w", err)
	}
	if err := tmpf.Chmod(0o600); err != nil {
		tmpf.Close()
		return fmt.Errorf("chmod temp key: %w", err)
	}
	if err := tmpf.Close(); err != nil {
		return fmt.Errorf("close temp key: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp key: %w", err)
	}
	cleanup = false // keep the file at its final path

	cryptCache.mu.Lock()
	cryptCache.key = key
	cryptCache.gcm = nil // force GCM rebuild with new key
	cryptCache.mu.Unlock()
	log.Printf("Session encryption key rotated successfully")
	return nil
}

func IsEncrypted(data []byte) bool {
	return bytes.HasPrefix(data, magicPrefix)
}

// DecryptFully repeatedly decrypts data until it is no longer encrypted,
// handling cases where data was encrypted multiple times (double encryption).
func DecryptFully(data []byte) ([]byte, error) {
	const maxIterations = 10
	for i := 0; i < maxIterations && IsEncrypted(data); i++ {
		var err error
		data, err = Decrypt(data)
		if err != nil {
			return nil, err
		}
	}
	if IsEncrypted(data) {
		return nil, fmt.Errorf("decrypt fully exceeded %d iterations", maxIterations)
	}
	return data, nil
}

// WriteEncryptedFile writes data to path, encrypted with AES-256-GCM.
// If encryption or key lookup fails, it returns the error and does not write
// any unencrypted data to disk.
func WriteEncryptedFile(path string, data []byte) error {
	encrypted, err := Encrypt(data)
	if err != nil {
		return fmt.Errorf("encrypt %s: %w", path, err)
	}
	return os.WriteFile(path, encrypted, 0o600)
}

// ReadEncryptedFile reads a file from path and, if it starts with the
// encryption magic prefix, decrypts it transparently.  Plaintext files
// (legacy or written by reasonix serve) are returned as-is.
func ReadEncryptedFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if IsEncrypted(data) {
		return Decrypt(data)
	}
	return data, nil
}
