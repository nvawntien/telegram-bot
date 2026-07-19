package inventorycrypto

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"testing"

	"github.com/nvawntien/telegram-bot/internal/app"
)

func TestCipherRoundTripAndRandomizedEnvelope(t *testing.T) {
	master := randomBytes(t, 32)
	secret := randomBytes(t, 48)
	cipher, err := New(master, 3, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	first, err := cipher.Protect(context.Background(), 41, secret)
	if err != nil {
		t.Fatalf("Protect() error = %v", err)
	}
	second, err := cipher.Protect(context.Background(), 41, secret)
	if err != nil {
		t.Fatalf("Protect() second error = %v", err)
	}
	if bytes.Contains(first.Ciphertext, secret) {
		t.Fatal("ciphertext contains plaintext")
	}
	if bytes.Equal(first.Nonce, second.Nonce) || bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Fatal("two encryption operations reused randomized envelope data")
	}
	plaintext, err := cipher.Decrypt(context.Background(), 41, first)
	if err != nil || !bytes.Equal(plaintext, secret) {
		t.Fatalf("Decrypt() = %x, %v", plaintext, err)
	}
}

func TestCipherRejectsWrongKeyVersionMetadataAndTampering(t *testing.T) {
	master := randomBytes(t, 32)
	secret := randomBytes(t, 32)
	cipher, err := New(master, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := cipher.Protect(context.Background(), 7, secret)
	if err != nil {
		t.Fatal(err)
	}

	wrongKey, err := New(randomBytes(t, 32), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongKey.Decrypt(context.Background(), 7, payload); !errors.Is(err, app.ErrDecryptionFailed) {
		t.Fatalf("wrong key error = %v", err)
	}
	if _, err := cipher.Decrypt(context.Background(), 8, payload); !errors.Is(err, app.ErrDecryptionFailed) {
		t.Fatalf("wrong product AAD error = %v", err)
	}

	wrongVersion := clonePayload(payload)
	wrongVersion.KeyVersion = 2
	if _, err := cipher.Decrypt(context.Background(), 7, wrongVersion); !errors.Is(err, app.ErrUnknownKeyVersion) {
		t.Fatalf("wrong key version error = %v", err)
	}
	tamperedCiphertext := clonePayload(payload)
	tamperedCiphertext.Ciphertext[0] ^= 0x80
	if _, err := cipher.Decrypt(context.Background(), 7, tamperedCiphertext); !errors.Is(err, app.ErrDecryptionFailed) {
		t.Fatalf("tampered ciphertext error = %v", err)
	}
	tamperedNonce := clonePayload(payload)
	tamperedNonce.Nonce[0] ^= 0x80
	if _, err := cipher.Decrypt(context.Background(), 7, tamperedNonce); !errors.Is(err, app.ErrDecryptionFailed) {
		t.Fatalf("tampered nonce error = %v", err)
	}
}

func TestCipherFingerprintSemanticsAndDomainSeparatedKeys(t *testing.T) {
	cipher, err := New(randomBytes(t, 32), 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := randomBytes(t, 27)
	first, _ := cipher.Protect(context.Background(), 10, secret)
	second, _ := cipher.Protect(context.Background(), 10, secret)
	differentPayload, _ := cipher.Protect(context.Background(), 10, append(append([]byte(nil), secret...), 1))
	differentProduct, _ := cipher.Protect(context.Background(), 11, secret)

	if !bytes.Equal(first.Fingerprint, second.Fingerprint) {
		t.Fatal("same product and payload produced different fingerprints")
	}
	if bytes.Equal(first.Fingerprint, differentPayload.Fingerprint) || bytes.Equal(first.Fingerprint, differentProduct.Fingerprint) {
		t.Fatal("fingerprint did not bind product and payload")
	}
	plainHash := sha256.Sum256(secret)
	if bytes.Equal(first.Fingerprint, plainHash[:]) {
		t.Fatal("fingerprint equals unkeyed SHA-256")
	}
	derived := cipher.keys[5]
	if bytes.Equal(derived.encryption, derived.fingerprint) {
		t.Fatal("encryption and fingerprint subkeys are equal")
	}
}

func TestCipherInputAndRandomFailurePolicies(t *testing.T) {
	if _, err := New(make([]byte, 31), 1, nil); !errors.Is(err, app.ErrEncryptionFailed) {
		t.Fatalf("invalid key length error = %v", err)
	}
	cipher, err := New(make([]byte, 32), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cipher.Protect(context.Background(), 1, nil); !errors.Is(err, app.ErrInvalidInventoryPayload) {
		t.Fatalf("empty plaintext error = %v", err)
	}
	failing, err := newWithRandom(map[int32][]byte{1: make([]byte, 32)}, 1, errorReader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.Protect(context.Background(), 1, []byte{1}); !errors.Is(err, app.ErrEncryptionFailed) {
		t.Fatalf("random source error = %v", err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	return value
}

func clonePayload(payload app.EncryptedInventoryPayload) app.EncryptedInventoryPayload {
	payload.Ciphertext = append([]byte(nil), payload.Ciphertext...)
	payload.Nonce = append([]byte(nil), payload.Nonce...)
	payload.Fingerprint = append([]byte(nil), payload.Fingerprint...)
	return payload
}
