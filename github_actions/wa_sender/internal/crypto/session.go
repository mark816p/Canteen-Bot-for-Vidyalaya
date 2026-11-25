package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// fileMagic identifies encrypted session blobs and versions the format.
	fileMagic = "WASN1"
	keySize   = 32 // AES-256
)

// ParseKey decodes a 256-bit key from either 64 hex chars or base64.
func ParseKey(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("encryption key is empty")
	}

	if len(trimmed) == hex.EncodedLen(keySize) {
		if b, err := hex.DecodeString(trimmed); err == nil && len(b) == keySize {
			return b, nil
		}
	}

	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(trimmed); err == nil && len(b) == keySize {
			return b, nil
		}
	}

	return nil, fmt.Errorf("encryption key must decode to exactly %d bytes (64 hex chars or 44-char base64)", keySize)
}

// EncryptBytes encrypts plaintext using AES-256-GCM with a random nonce.
func EncryptBytes(plaintext, key []byte) ([]byte, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("invalid key length %d, expected %d", len(key), keySize)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	out := make([]byte, 0, len(fileMagic)+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, fileMagic...)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, []byte(fileMagic))
	return out, nil
}

// DecryptBytes decrypts and authenticates an AES-256-GCM blob produced by EncryptBytes.
func DecryptBytes(blob, key []byte) ([]byte, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("invalid key length %d, expected %d", len(key), keySize)
	}
	if len(blob) < len(fileMagic) || string(blob[:len(fileMagic)]) != fileMagic {
		return nil, errors.New("invalid encrypted session header")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	headerLen := len(fileMagic) + gcm.NonceSize()
	if len(blob) < headerLen+gcm.Overhead() {
		return nil, errors.New("encrypted session file is truncated")
	}

	nonce := blob[len(fileMagic):headerLen]
	ciphertext := blob[headerLen:]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(fileMagic))
	if err != nil {
		return nil, fmt.Errorf("decrypt session (wrong key or tampered file): %w", err)
	}
	return plaintext, nil
}

// DecryptToTemp decrypts encPath (if it exists) into a 0600 temp file and returns
// the temp path plus a cleanup function that overwrites and deletes the temp DB
// and any SQLite sidecar files.
func DecryptToTemp(encPath string, key []byte, allowMissing bool) (string, func(), error) {
	tmpFile, err := os.CreateTemp("", "wa-session-*.db")
	if err != nil {
		return "", nil, fmt.Errorf("create temp session file: %w", err)
	}
	tempPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", nil, fmt.Errorf("close temp file: %w", err)
	}
	_ = os.Chmod(tempPath, 0o600)

	cleanup := func() {
		for _, p := range []string{tempPath, tempPath + "-wal", tempPath + "-shm", tempPath + "-journal"} {
			shredAndRemove(p)
		}
	}

	blob, err := os.ReadFile(encPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && allowMissing {
			return tempPath, cleanup, nil
		}
		cleanup()
		return "", nil, fmt.Errorf("read encrypted session %q: %w", encPath, err)
	}

	plaintext, err := DecryptBytes(blob, key)
	if err != nil {
		cleanup()
		return "", nil, err
	}

	if err := os.WriteFile(tempPath, plaintext, 0o600); err != nil {
		zeroBytes(plaintext)
		cleanup()
		return "", nil, fmt.Errorf("write temp session DB: %w", err)
	}
	zeroBytes(plaintext)

	return tempPath, cleanup, nil
}

// EncryptFromFile encrypts plainPath and atomically writes it to encPath with 0600 permissions.
func EncryptFromFile(plainPath, encPath string, key []byte) error {
	plaintext, err := os.ReadFile(plainPath)
	if err != nil {
		return fmt.Errorf("read plaintext session DB: %w", err)
	}
	defer zeroBytes(plaintext)

	blob, err := EncryptBytes(plaintext, key)
	if err != nil {
		return err
	}

	dir := filepath.Dir(encPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}

	tmpEnc := encPath + ".tmp"
	if err := os.WriteFile(tmpEnc, blob, 0o600); err != nil {
		return fmt.Errorf("write temp encrypted file: %w", err)
	}

	if err := os.Rename(tmpEnc, encPath); err != nil {
		_ = os.Remove(tmpEnc)
		return fmt.Errorf("replace encrypted session file: %w", err)
	}
	return nil
}

func shredAndRemove(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if size := info.Size(); size > 0 {
		if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			zeros := make([]byte, 4096)
			for remaining := size; remaining > 0; {
				chunk := int64(len(zeros))
				if remaining < chunk {
					chunk = remaining
				}
				if _, werr := f.Write(zeros[:chunk]); werr != nil {
					break
				}
				remaining -= chunk
			}
			_ = f.Sync()
			_ = f.Close()
		}
	}
	_ = os.Remove(path)
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
