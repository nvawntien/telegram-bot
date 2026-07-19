package app

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestPaymentReferenceFormat(t *testing.T) {
	generator, err := NewPaymentReferenceGenerator("ts", 6)
	if err != nil {
		t.Fatal(err)
	}
	generator.random = bytes.NewReader([]byte{0, 1, 2, 10, 11, 255})
	reference, err := generator.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if reference != "TS0001020A0BFF" {
		t.Fatalf("reference = %q", reference)
	}
}

func TestPaymentReferenceValidationAndRandomFailure(t *testing.T) {
	if _, err := NewPaymentReferenceGenerator("bad-prefix", 6); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid prefix error = %v", err)
	}
	generator, err := NewPaymentReferenceGenerator("TS", 6)
	if err != nil {
		t.Fatal(err)
	}
	generator.random = failingReferenceReader{}
	if _, err := generator.Generate(); !errors.Is(err, ErrPaymentReferenceCollision) {
		t.Fatalf("random failure error = %v", err)
	}
}

type failingReferenceReader struct{}

func (failingReferenceReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
