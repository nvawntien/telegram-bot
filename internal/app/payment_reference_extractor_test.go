package app

import (
	"strings"
	"testing"
)

func TestPaymentReferenceExtractor(t *testing.T) {
	extractor, err := NewPaymentReferenceExtractor("PAY", 4, 64)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		content   string
		match     PaymentReferenceMatch
		reference string
	}{
		{name: "only token", content: "PAYA1B2C3D4", match: PaymentReferenceExact, reference: "PAYA1B2C3D4"},
		{name: "case normalized", content: "payment paya1b2c3d4 received", match: PaymentReferenceExact, reference: "PAYA1B2C3D4"},
		{name: "punctuation boundaries", content: "memo:PAYA1B2C3D4.", match: PaymentReferenceExact, reference: "PAYA1B2C3D4"},
		{name: "unicode boundaries", content: "chuyen tien | PAYA1B2C3D4 | thanh cong", match: PaymentReferenceExact, reference: "PAYA1B2C3D4"},
		{name: "embedded substring rejected", content: "XXPAYA1B2C3D4YY", match: PaymentReferenceMissing},
		{name: "too short", content: "PAYA1B2", match: PaymentReferenceInvalid},
		{name: "non hex", content: "PAYA1B2C3ZZ", match: PaymentReferenceInvalid},
		{name: "multiple", content: "PAYA1B2C3D4 PAY00112233", match: PaymentReferenceMultiple},
		{name: "valid plus malformed", content: "PAYA1B2C3D4 PAYA1B2ZZZZ", match: PaymentReferenceMultiple},
		{name: "missing", content: "ordinary bank transfer", match: PaymentReferenceMissing},
		{name: "no fuzzy separator", content: "PAY A1B2C3D4", match: PaymentReferenceMissing},
		{name: "oversized", content: strings.Repeat("x", 65), match: PaymentReferenceInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := extractor.Extract(test.content)
			if result.Match != test.match || result.Reference != test.reference {
				t.Fatalf("Extract(%q) = %+v", test.content, result)
			}
		})
	}
}

func TestNewPaymentReferenceExtractorRejectsUnsafeConfiguration(t *testing.T) {
	for _, test := range []struct {
		prefix      string
		randomBytes int
		limit       int
	}{
		{prefix: "", randomBytes: 4, limit: 64},
		{prefix: "PAY CODE", randomBytes: 4, limit: 64},
		{prefix: "PAY", randomBytes: 3, limit: 64},
		{prefix: "PAY", randomBytes: 25, limit: 64},
		{prefix: "PAY", randomBytes: 4, limit: 0},
		{prefix: "PAY", randomBytes: 4, limit: 8193},
	} {
		if _, err := NewPaymentReferenceExtractor(test.prefix, test.randomBytes, test.limit); err == nil {
			t.Fatalf("NewPaymentReferenceExtractor(%q, %d, %d) succeeded", test.prefix, test.randomBytes, test.limit)
		}
	}
}
