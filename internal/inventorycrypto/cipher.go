// Package inventorycrypto protects opaque digital inventory payloads.
package inventorycrypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/nvawntien/telegram-bot/internal/app"
)

const (
	FormatAES256GCMV1 = "aes-256-gcm-v1"
	masterKeyBytes    = 32
	nonceBytes        = 12

	encryptionInfo  = "telegram-shop/inventory/encryption/v1"
	fingerprintInfo = "telegram-shop/inventory/fingerprint/v1"
)

type Metrics interface {
	ObserveInventoryEncryption(operation, result string)
}

type derivedKey struct {
	encryption  []byte
	fingerprint []byte
}

type Cipher struct {
	keys           map[int32]derivedKey
	currentVersion int32
	random         io.Reader
	metrics        Metrics
}

func New(masterKey []byte, keyVersion int32, metrics Metrics) (*Cipher, error) {
	return newWithRandom(map[int32][]byte{keyVersion: masterKey}, keyVersion, rand.Reader, metrics)
}

// NewKeyring supports reading more than one explicitly configured key version.
// Phase 4 runtime supplies one key; this constructor is the narrow seam needed
// for future rotation without putting keys in PostgreSQL.
func NewKeyring(masterKeys map[int32][]byte, currentVersion int32, metrics Metrics) (*Cipher, error) {
	return newWithRandom(masterKeys, currentVersion, rand.Reader, metrics)
}

func newWithRandom(masterKeys map[int32][]byte, currentVersion int32, random io.Reader, metrics Metrics) (*Cipher, error) {
	if currentVersion <= 0 || random == nil || len(masterKeys) == 0 {
		return nil, app.ErrEncryptionFailed
	}
	keys := make(map[int32]derivedKey, len(masterKeys))
	for version, master := range masterKeys {
		if version <= 0 || len(master) != masterKeyBytes {
			return nil, app.ErrEncryptionFailed
		}
		encryptionKey, err := hkdf.Key(sha256.New, master, nil, encryptionInfo, masterKeyBytes)
		if err != nil {
			return nil, app.ErrEncryptionFailed
		}
		fingerprintKey, err := hkdf.Key(sha256.New, master, nil, fingerprintInfo, masterKeyBytes)
		if err != nil {
			return nil, app.ErrEncryptionFailed
		}
		keys[version] = derivedKey{encryption: encryptionKey, fingerprint: fingerprintKey}
	}
	if _, exists := keys[currentVersion]; !exists {
		return nil, app.ErrUnknownKeyVersion
	}
	return &Cipher{keys: keys, currentVersion: currentVersion, random: random, metrics: metrics}, nil
}

func (c *Cipher) Protect(ctx context.Context, productID int64, plaintext []byte) (app.EncryptedInventoryPayload, error) {
	if err := ctx.Err(); err != nil {
		c.observe("encrypt", "cancelled")
		return app.EncryptedInventoryPayload{}, fmt.Errorf("protect inventory: %w", err)
	}
	if productID <= 0 || len(plaintext) == 0 {
		c.observe("encrypt", "invalid")
		return app.EncryptedInventoryPayload{}, app.ErrInvalidInventoryPayload
	}
	key, exists := c.keys[c.currentVersion]
	if !exists {
		c.observe("encrypt", "unknown_key")
		return app.EncryptedInventoryPayload{}, app.ErrUnknownKeyVersion
	}
	gcm, err := newGCM(key.encryption)
	if err != nil {
		c.observe("encrypt", "failed")
		return app.EncryptedInventoryPayload{}, app.ErrEncryptionFailed
	}
	nonce := make([]byte, nonceBytes)
	if _, err := io.ReadFull(c.random, nonce); err != nil {
		c.observe("encrypt", "random_failed")
		return app.EncryptedInventoryPayload{}, app.ErrEncryptionFailed
	}
	aad := associatedData(c.currentVersion, productID)
	ciphertext := gcm.Seal(nil, nonce, plaintext, aad)
	fingerprint := keyedFingerprint(key.fingerprint, productID, plaintext)
	c.observe("encrypt", "success")
	return app.EncryptedInventoryPayload{
		Ciphertext: ciphertext, Nonce: nonce, Fingerprint: fingerprint,
		KeyVersion: c.currentVersion, Format: FormatAES256GCMV1,
	}, nil
}

func (c *Cipher) Decrypt(ctx context.Context, productID int64, payload app.EncryptedInventoryPayload) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		c.observe("decrypt", "cancelled")
		return nil, fmt.Errorf("decrypt inventory: %w", err)
	}
	if productID <= 0 || payload.Format != FormatAES256GCMV1 || len(payload.Nonce) != nonceBytes || len(payload.Ciphertext) < 16 {
		c.observe("decrypt", "invalid")
		return nil, app.ErrDecryptionFailed
	}
	key, exists := c.keys[payload.KeyVersion]
	if !exists {
		c.observe("decrypt", "unknown_key")
		return nil, app.ErrUnknownKeyVersion
	}
	gcm, err := newGCM(key.encryption)
	if err != nil {
		c.observe("decrypt", "failed")
		return nil, app.ErrDecryptionFailed
	}
	plaintext, err := gcm.Open(nil, payload.Nonce, payload.Ciphertext, associatedData(payload.KeyVersion, productID))
	if err != nil {
		c.observe("decrypt", "authentication_failed")
		return nil, app.ErrDecryptionFailed
	}
	c.observe("decrypt", "success")
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func associatedData(keyVersion int32, productID int64) []byte {
	const prefix = "telegram-shop/inventory/aad/v1"
	result := make([]byte, len(prefix)+1+4+8)
	copy(result, prefix)
	result[len(prefix)] = 0
	binary.BigEndian.PutUint32(result[len(prefix)+1:], uint32(keyVersion))
	binary.BigEndian.PutUint64(result[len(prefix)+5:], uint64(productID))
	return result
}

func keyedFingerprint(key []byte, productID int64, plaintext []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("telegram-shop/inventory/fingerprint-input/v1"))
	_, _ = mac.Write([]byte{0})
	var metadata [16]byte
	binary.BigEndian.PutUint64(metadata[:8], uint64(productID))
	binary.BigEndian.PutUint64(metadata[8:], uint64(len(plaintext)))
	_, _ = mac.Write(metadata[:])
	_, _ = mac.Write(plaintext)
	return mac.Sum(nil)
}

func (c *Cipher) observe(operation, result string) {
	if c.metrics != nil {
		c.metrics.ObserveInventoryEncryption(operation, result)
	}
}

var _ app.InventoryCipher = (*Cipher)(nil)
