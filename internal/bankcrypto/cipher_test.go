package bankcrypto

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/nvawntien/telegram-bot/internal/app"
)

func TestCipherProtectDecryptAndFingerprint(t *testing.T) {
	cipher, err := New(bytes.Repeat([]byte{0x42}, 32), 3)
	if err != nil {
		t.Fatal(err)
	}
	first, err := cipher.Protect(context.Background(), "970422", "1234567890")
	if err != nil {
		t.Fatal(err)
	}
	second, err := cipher.Protect(context.Background(), "970422", "1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Nonce, second.Nonce) || bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Fatal("randomized envelopes reused nonce or ciphertext")
	}
	if !bytes.Equal(first.Fingerprint, second.Fingerprint) {
		t.Fatal("business-key fingerprint is not stable")
	}
	plaintext, err := cipher.Decrypt(context.Background(), "970422", first)
	if err != nil || plaintext != "1234567890" {
		t.Fatalf("Decrypt() = %q, %v", plaintext, err)
	}
}

func TestCipherBindsBankAndRejectsInvalidInput(t *testing.T) {
	cipher, err := New(bytes.Repeat([]byte{0x24}, 32), 1)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := cipher.Protect(context.Background(), "970422", "1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cipher.Decrypt(context.Background(), "970415", protected); !errors.Is(err, app.ErrBankDecryptionFailed) {
		t.Fatalf("wrong bank error = %v", err)
	}
	if _, err := cipher.Protect(context.Background(), "ABC", "123"); !errors.Is(err, app.ErrBankEncryptionFailed) {
		t.Fatalf("invalid input error = %v", err)
	}
}
