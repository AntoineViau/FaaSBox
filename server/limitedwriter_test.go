package main

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestTailWriter_UnderLimit(t *testing.T) {
	w := newTailWriter(16)

	n, err := w.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if n != 5 {
		t.Errorf("Write() = %d, want 5", n)
	}
	if w.truncated() {
		t.Error("truncated() is true although the limit was not reached")
	}
	if got := w.String(); got != "hello" {
		t.Errorf("String() = %q, want %q — nothing was dropped, so no marker", got, "hello")
	}
}

func TestTailWriter_ExactlyAtLimit(t *testing.T) {
	w := newTailWriter(5)

	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	// Filling the buffer exactly is not a truncation: nothing was dropped.
	if w.truncated() {
		t.Error("truncated() is true although no byte was dropped")
	}
	if got := w.String(); got != "hello" {
		t.Errorf("String() = %q, want %q", got, "hello")
	}
}

// TestTailWriter_KeepsTheEnd is the whole reason this writer exists: bun install
// prints its progress first and its failure last, so a capture that keeps the
// head keeps the half nobody needs.
func TestTailWriter_KeepsTheEnd(t *testing.T) {
	w := newTailWriter(10)

	if _, err := w.Write([]byte("noise-noise-noise-error: the last line")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if !w.truncated() {
		t.Fatal("truncated() is false although the write crossed the limit")
	}
	if got := w.String(); !strings.HasSuffix(got, " last line") {
		t.Errorf("String() = %q, want it to end on the last bytes written", got)
	}
	if got := w.String(); strings.Contains(got, "noise") {
		t.Errorf("String() = %q, want the head dropped", got)
	}
}

func TestTailWriter_AccumulatesAcrossWrites(t *testing.T) {
	w := newTailWriter(10)

	// The stream arrives in pieces, as an exec pump delivers it: the tail has to
	// be the tail of the whole stream, not of the last write.
	for _, chunk := range []string{"aaaa", "bbbb", "cccc", "dddd"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write(%q) error = %v", chunk, err)
		}
	}
	if got := w.String(); !strings.HasSuffix(got, "bbccccdddd") {
		t.Errorf("String() = %q, want it to end on %q", got, "bbccccdddd")
	}
	if w.total != 16 {
		t.Errorf("total = %d, want the 16 bytes written", w.total)
	}
}

func TestTailWriter_MarkerCarriesTheOriginalSize(t *testing.T) {
	w := newTailWriter(8)

	if _, err := w.Write([]byte(strings.Repeat("x", 500))); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := w.String()
	// The marker sits at the head because that is the end the cut happened on.
	if !strings.HasPrefix(got, "...[truncated, 500 bytes total]\n") {
		t.Errorf("String() = %q, want a leading marker stating 500 bytes", got)
	}
	if !strings.HasSuffix(got, strings.Repeat("x", 8)) {
		t.Errorf("String() = %q, want the marker followed by the kept tail", got)
	}
}

// TestTailWriter_BoundsMemory is the dependency of the whole design: bun install
// used to fill an unbounded bytes.Buffer, held down by nothing but depsTimeout.
func TestTailWriter_BoundsMemory(t *testing.T) {
	w := newTailWriter(64)

	chunk := []byte(strings.Repeat("y", 4096))
	for i := 0; i < 256; i++ { // 1 MB through a 64-byte window
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}

	if len(w.buf) > 64 {
		t.Errorf("buffered %d bytes, want at most the 64-byte cap", len(w.buf))
	}
	if cap(w.buf) > 64 {
		t.Errorf("buffer capacity grew to %d, want it pinned at the 64-byte cap", cap(w.buf))
	}
	if w.total != 256*4096 {
		t.Errorf("total = %d, want the full %d bytes counted", w.total, 256*4096)
	}
}

func TestTailWriter_ReportsFullWrites(t *testing.T) {
	w := newTailWriter(4)

	// Reporting a short write makes io.Copy and exec's stream pumps fail the
	// command — a bounded capture would turn into a spurious install failure.
	n, err := w.Write([]byte(strings.Repeat("z", 100)))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if n != 100 {
		t.Errorf("Write() = %d, want the full 100 bytes reported as accepted", n)
	}
}

func TestTailWriter_KeepsValidUTF8AtTheCut(t *testing.T) {
	// Six 3-byte runes; a 10-byte window cuts inside the second one.
	w := newTailWriter(10)

	if _, err := w.Write([]byte(strings.Repeat("é", 4) + strings.Repeat("ü", 2))); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := w.String()
	if !utf8.ValidString(got) {
		t.Errorf("String() = %q, want valid UTF-8 after a cut inside a rune", got)
	}
	if !strings.HasSuffix(got, "üü") {
		t.Errorf("String() = %q, want the trailing runes intact", got)
	}
}

func TestTailWriter_ZeroCapKeepsNothing(t *testing.T) {
	w := newTailWriter(0)

	if _, err := w.Write([]byte("dropped")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got := w.String(); got != "...[truncated, 7 bytes total]\n" {
		t.Errorf("String() = %q, want the marker alone", got)
	}
}
