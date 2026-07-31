package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLimitedWriter_UnderLimit(t *testing.T) {
	w := &limitedWriter{max: 16}

	n, err := w.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if n != 5 {
		t.Errorf("Write() = %d, want 5", n)
	}
	if w.truncated {
		t.Error("truncated is set although the limit was not reached")
	}
	if got := w.String(); got != "hello" {
		t.Errorf("String() = %q, want %q", got, "hello")
	}
	if got := w.Len(); got != 5 {
		t.Errorf("Len() = %d, want 5", got)
	}
	if got := w.Bytes(); !bytes.Equal(got, []byte("hello")) {
		t.Errorf("Bytes() = %q, want %q", got, "hello")
	}
}

func TestLimitedWriter_ExactlyAtLimit(t *testing.T) {
	w := &limitedWriter{max: 5}

	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	// Filling the buffer exactly is not a truncation: nothing was dropped.
	if w.truncated {
		t.Error("truncated is set although no byte was dropped")
	}
	if got := w.Len(); got != 5 {
		t.Errorf("Len() = %d, want 5", got)
	}
}

func TestLimitedWriter_WriteCrossingLimit(t *testing.T) {
	w := &limitedWriter{max: 10}

	// The caller must always be told its whole write was accepted: reporting a
	// short write makes io.Copy and exec's stream pumps fail the command.
	n, err := w.Write([]byte(strings.Repeat("a", 25)))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if n != 25 {
		t.Errorf("Write() = %d, want the full 25 bytes reported as accepted", n)
	}
	if !w.truncated {
		t.Error("truncated is not set although the write crossed the limit")
	}
	if got := w.Len(); got != 10 {
		t.Errorf("Len() = %d, want the buffer capped at 10", got)
	}
	if got := w.String(); got != strings.Repeat("a", 10) {
		t.Errorf("String() = %q, want the first 10 bytes", got)
	}
}

func TestLimitedWriter_WriteOnceFull(t *testing.T) {
	w := &limitedWriter{max: 4}

	if _, err := w.Write([]byte("abcd")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	n, err := w.Write([]byte("efgh"))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if n != 4 {
		t.Errorf("Write() = %d, want 4", n)
	}
	if !w.truncated {
		t.Error("truncated is not set although a write landed on a full buffer")
	}
	if got := w.String(); got != "abcd" {
		t.Errorf("String() = %q, want the buffer untouched past the limit", got)
	}
}

func TestLimitedWriter_AccumulatesUpToLimit(t *testing.T) {
	w := &limitedWriter{max: 10}

	for i := 0; i < 4; i++ {
		if _, err := w.Write([]byte("abc")); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if !w.truncated {
		t.Error("truncated is not set although 12 bytes were written into a 10-byte buffer")
	}
	if got := w.String(); got != "abcabcabca" {
		t.Errorf("String() = %q, want %q", got, "abcabcabca")
	}
}

// TestNewLimitedWriter_UsesConfiguredCap ties the constructor to the tunable.
// The cap is read once at startup, so the constructor must not carry a bound
// of its own that would ignore FAASBOX_MAX_OUTPUT_SIZE.
func TestNewLimitedWriter_UsesConfiguredCap(t *testing.T) {
	w := newLimitedWriter()
	if w.max != maxOutputSize {
		t.Errorf("newLimitedWriter().max = %d, want maxOutputSize (%d)", w.max, maxOutputSize)
	}
	if w.truncated {
		t.Error("a fresh writer reports a truncation")
	}
	if w.Len() != 0 {
		t.Errorf("a fresh writer has %d bytes buffered, want 0", w.Len())
	}
}
