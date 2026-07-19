// Package bankcrypto protects bank account numbers at rest.
package bankcrypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"io"

	"github.com/nvawntien/telegram-bot/internal/app"
)

const (
	FormatAES256GCMV1 = "aes-256-gcm-v1"
	masterKeyBytes    = 32
	nonceBytes        = 12
	keyIDPrefix       = "bank-v"
)

type derivedKey struct {
	encryption  []byte
	fingerprint []byte
}

type Cipher struct {
	keys           map[int32]derivedKey
	currentVersion int32
	random         io.Reader
}

func New(masterKey []byte, keyVersion int32) (*Cipher, error) {
	return newWithRandom(map[int32][]byte{keyVersion: masterKey}, keyVersion, rand.Reader)
}

func NewKeyring(masterKeys map[int32][]byte, currentVersion int32) (*Cipher, error) {
	return newWithRandom(masterKeys, currentVersion, rand.Reader)
}

func newWithRandom(masterKeys map[int32][]byte, currentVersion int32, random io.Reader) (*Cipher, error) {
	if currentVersion <= 0 || random == nil || len(masterKeys) == 0 {
		return nil, app.ErrBankEncryptionFailed
	}
	keys := make(map[int32]derivedKey, len(masterKeys))
	for version, master := range masterKeys {
		if version <= 0 || len(master) != masterKeyBytes {
			return nil, app.ErrBankEncryptionFailed
		}
		encryptionKey, err := hkdf.Key(sha256.New, master, nil, "telegram-shop/bank/encryption/v1", masterKeyBytes)
		if err != nil {
			return nil, app.ErrBankEncryptionFailed
		}
		fingerprintKey, err := hkdf.Key(sha256.New, master, nil, "telegram-shop/bank/fingerprint/v1", masterKeyBytes)
		if err != nil {
			return nil, app.ErrBankEncryptionFailed
		}
		keys[version] = derivedKey{encryption: encryptionKey, fingerprint: fingerprintKey}
	}
	if _, ok := keys[currentVersion]; !ok {
		return nil, app.ErrUnknownBankKeyVersion
	}
	return &Cipher{keys: keys, currentVersion: currentVersion, random: random}, nil
}

func (c *Cipher) Protect(ctx context.Context, bankBIN, accountNumber string) (app.ProtectedBankAccountNumber, error) {
	if ctx.Err() != nil || !validBankBIN(bankBIN) || !validAccountNumber(accountNumber) {
		return app.ProtectedBankAccountNumber{}, app.ErrBankEncryptionFailed
	}
	key, ok := c.keys[c.currentVersion]
	if !ok {
		return app.ProtectedBankAccountNumber{}, app.ErrUnknownBankKeyVersion
	}
	gcm, err := newGCM(key.encryption)
	if err != nil {
		return app.ProtectedBankAccountNumber{}, app.ErrBankEncryptionFailed
	}
	nonce := make([]byte, nonceBytes)
	if _, err := io.ReadFull(c.random, nonce); err != nil {
		return app.ProtectedBankAccountNumber{}, app.ErrBankEncryptionFailed
	}
	plaintext := []byte(accountNumber)
	return app.ProtectedBankAccountNumber{
		Ciphertext:  gcm.Seal(nil, nonce, plaintext, associatedData(c.currentVersion, bankBIN)),
		Nonce:       nonce,
		Fingerprint: fingerprint(key.fingerprint, bankBIN, plaintext),
		KeyVersion:  c.currentVersion,
		KeyID:       keyIDPrefix + decimalVersion(c.currentVersion),
		Format:      FormatAES256GCMV1,
	}, nil
}

func (c *Cipher) Decrypt(ctx context.Context, bankBIN string, protected app.ProtectedBankAccountNumber) (string, error) {
	if ctx.Err() != nil || !validBankBIN(bankBIN) || protected.Format != FormatAES256GCMV1 || len(protected.Nonce) != nonceBytes || len(protected.Ciphertext) < 16 {
		return "", app.ErrBankDecryptionFailed
	}
	key, ok := c.keys[protected.KeyVersion]
	if !ok {
		return "", app.ErrUnknownBankKeyVersion
	}
	gcm, err := newGCM(key.encryption)
	if err != nil {
		return "", app.ErrBankDecryptionFailed
	}
	plaintext, err := gcm.Open(nil, protected.Nonce, protected.Ciphertext, associatedData(protected.KeyVersion, bankBIN))
	if err != nil || !validAccountNumber(string(plaintext)) {
		return "", app.ErrBankDecryptionFailed
	}
	return string(plaintext), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func associatedData(version int32, bankBIN string) []byte {
	result := make([]byte, 4+len(bankBIN))
	binary.BigEndian.PutUint32(result, uint32(version))
	copy(result[4:], bankBIN)
	return result
}

func fingerprint(key []byte, bankBIN string, accountNumber []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("telegram-shop/bank/fingerprint-input/v1"))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(bankBIN))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(accountNumber)
	return mac.Sum(nil)
}

func validBankBIN(value string) bool {
	return len(value) == 6 && digitsOnly(value)
}

func validAccountNumber(value string) bool {
	return len(value) >= 4 && len(value) <= 34 && digitsOnly(value)
}

func digitsOnly(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func decimalVersion(value int32) string {
	if value == 0 {
		return "0"
	}
	var buffer [10]byte
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[position:])
}

var _ app.BankAccountCipher = (*Cipher)(nil)
