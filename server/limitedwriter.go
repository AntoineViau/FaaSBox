package main

import (
	"bytes"
	"log"
	"os"
	"strconv"
)

const defaultMaxOutputSize = 10 << 20 // 10 MB

// maxOutputSize is the number of bytes captured per subprocess stream.
// Configurable via FAASBOX_MAX_OUTPUT_SIZE environment variable. The cap is
// surfaced to callers whose output it invalidates, so it has to be adjustable
// without a rebuild.
var maxOutputSize = func() int {
	s := os.Getenv("FAASBOX_MAX_OUTPUT_SIZE")
	if s == "" {
		return defaultMaxOutputSize
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		log.Printf("faasbox: invalid FAASBOX_MAX_OUTPUT_SIZE=%q, using default %d", s, defaultMaxOutputSize)
		return defaultMaxOutputSize
	}
	return n
}()

// limitedWriter captures subprocess output up to a maximum size.
// Beyond the limit, writes are silently discarded and truncated is set to true.
type limitedWriter struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func newLimitedWriter() *limitedWriter {
	return &limitedWriter{max: maxOutputSize}
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.buf.Len() >= w.max {
		w.truncated = true
		return len(p), nil
	}
	if w.buf.Len()+len(p) > w.max {
		w.truncated = true
		remaining := w.max - w.buf.Len()
		w.buf.Write(p[:remaining])
		return len(p), nil
	}
	return w.buf.Write(p)
}

func (w *limitedWriter) String() string {
	return w.buf.String()
}

func (w *limitedWriter) Bytes() []byte {
	return w.buf.Bytes()
}

func (w *limitedWriter) Len() int {
	return w.buf.Len()
}
