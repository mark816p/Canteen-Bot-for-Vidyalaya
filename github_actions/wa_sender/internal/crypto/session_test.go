package crypto

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestParseKey(t *testing.T) {
	raw := bytes.Repeat([]byte{0x42}, keySize)

	hexKey, err := ParseKey(hex.EncodeToString(raw))
	if err != nil || !bytes.Equal(hexKey, raw) {
		t.Fatalf("expected valid hex key, got err=%v", err)
	}

	b64Key, err := ParseKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil || !bytes.Equal(b64Key, raw) {
		t.Fatalf("expected valid base64 key, got err=%v", err)
	}

	if _, err := ParseKey("short"); err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0xAB}, keySize)
	wrongKey := bytes.Repeat([]byte{0xCD}, keySize)
	plaintext := []byte("sqlite-format-3-session-payload")

	blob, err := EncryptBytes(plaintext, key)
	if err != nil {
		t.Fatalf("EncryptBytes failed: %v", err)
	}

	got, err := DecryptBytes(blob, key)
	if err != nil {
		t.Fatalf("DecryptBytes failed: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q, want %q", got, plaintext)
	}

	if _, err := DecryptBytes(blob, wrongKey); err == nil {
		t.Fatal("expected error with wrong key")
	}

	tampered := append([]byte(nil), blob...)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := DecryptBytes(tampered, key); err == nil {
		t.Fatal("expected error with tampered ciphertext")
	}
}

func TestFileRoundTripAndCleanup(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{0x19}, keySize)
	srcPath := filepath.Join(dir, "plain.db")
	encPath := filepath.Join(dir, "session.db.enc")
	want := []byte("mock-sqlite-db-contents")

	if err := os.WriteFile(srcPath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EncryptFromFile(srcPath, encPath, key); err != nil {
		t.Fatalf("EncryptFromFile failed: %v", err)
	}

	tmpPath, cleanup, err := DecryptToTemp(encPath, key, false)
	if err != nil {
		t.Fatalf("DecryptToTemp failed: %v", err)
	}
	got, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file round-trip mismatch: got %q, want %q", got, want)
	}

	cleanup()
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("expected temp file %q to be deleted after cleanup", tmpPath)
	}
}
