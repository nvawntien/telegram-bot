package app

import (
	"strings"
	"unicode"
)

const DefaultPaymentTransferContentLimit = 2048

type PaymentReferenceMatch string

const (
	PaymentReferenceExact    PaymentReferenceMatch = "exact"
	PaymentReferenceMissing  PaymentReferenceMatch = "missing"
	PaymentReferenceMultiple PaymentReferenceMatch = "multiple"
	PaymentReferenceInvalid  PaymentReferenceMatch = "invalid"
)

type PaymentReferenceExtraction struct {
	Match     PaymentReferenceMatch
	Reference string
}

type PaymentReferenceExtractor struct {
	prefix       string
	tokenLength  int
	contentLimit int
}

func NewPaymentReferenceExtractor(prefix string, randomBytes, contentLimit int) (*PaymentReferenceExtractor, error) {
	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	if !safeReferencePart(prefix) || randomBytes < 4 || randomBytes > 24 || contentLimit <= 0 || contentLimit > 8192 {
		return nil, ErrInvalidInput
	}
	tokenLength := len(prefix) + randomBytes*2
	return &PaymentReferenceExtractor{
		prefix: prefix, tokenLength: tokenLength, contentLimit: contentLimit,
	}, nil
}

func (e *PaymentReferenceExtractor) Extract(content string) PaymentReferenceExtraction {
	if e == nil || content == "" {
		return PaymentReferenceExtraction{Match: PaymentReferenceMissing}
	}
	if len([]byte(content)) > e.contentLimit {
		return PaymentReferenceExtraction{Match: PaymentReferenceInvalid}
	}
	tokens := strings.FieldsFunc(content, func(value rune) bool {
		return !unicode.IsLetter(value) && !unicode.IsDigit(value) && value != '-'
	})
	valid := make([]string, 0, 1)
	invalid := false
	for _, raw := range tokens {
		token := strings.ToUpper(raw)
		if !strings.HasPrefix(token, e.prefix) {
			continue
		}
		suffix := token[len(e.prefix):]
		if suffix == "" || !isUpperHex(rune(suffix[0])) {
			continue
		}
		if len(token) == e.tokenLength && allUpperHex(suffix) {
			valid = append(valid, token)
		} else {
			invalid = true
		}
	}
	if len(valid) > 1 {
		return PaymentReferenceExtraction{Match: PaymentReferenceMultiple}
	}
	if len(valid) == 1 {
		if invalid {
			return PaymentReferenceExtraction{Match: PaymentReferenceMultiple}
		}
		return PaymentReferenceExtraction{Match: PaymentReferenceExact, Reference: valid[0]}
	}
	if invalid {
		return PaymentReferenceExtraction{Match: PaymentReferenceInvalid}
	}
	return PaymentReferenceExtraction{Match: PaymentReferenceMissing}
}

func allUpperHex(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !isUpperHex(char) {
			return false
		}
	}
	return true
}

func isUpperHex(char rune) bool {
	return (char >= '0' && char <= '9') || (char >= 'A' && char <= 'F')
}
