package app

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"strings"
)

type PaymentReferenceGenerator interface {
	Generate() (string, error)
}

type RandomPaymentReferenceGenerator struct {
	prefix string
	bytes  int
	random io.Reader
}

func NewPaymentReferenceGenerator(prefix string, randomBytes int) (*RandomPaymentReferenceGenerator, error) {
	prefix = strings.TrimSpace(strings.ToUpper(prefix))
	if !safeReferencePart(prefix) || randomBytes < 4 || randomBytes > 24 {
		return nil, ErrInvalidInput
	}
	return &RandomPaymentReferenceGenerator{prefix: prefix, bytes: randomBytes, random: rand.Reader}, nil
}

func (g *RandomPaymentReferenceGenerator) Generate() (string, error) {
	buffer := make([]byte, g.bytes)
	if _, err := io.ReadFull(g.random, buffer); err != nil {
		return "", ErrPaymentReferenceCollision
	}
	return g.prefix + strings.ToUpper(hex.EncodeToString(buffer)), nil
}

func safeReferencePart(value string) bool {
	if len(value) < 1 || len(value) > 12 {
		return false
	}
	for _, char := range value {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

var _ PaymentReferenceGenerator = (*RandomPaymentReferenceGenerator)(nil)
