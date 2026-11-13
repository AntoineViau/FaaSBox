package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// What a function served at /f/ writes on stdout: the mirror of the envelope it
// reads on stdin.
//
//	{ "status": 302, "headers": { "location": "/f/home" }, "body": "" }
//
// /invoke wraps whatever a function prints into {function, result, duration_ms},
// which suits a program calling a program and nothing else: a page needs its own
// content-type, a redirect its status and its Location, a webhook its 401. The
// envelope is how a function says all three, and it is only read on the route
// that serves a function to the public — /invoke and MCP decode stdout as they
// always did.
//
// **"body" is always a string**, as it is on the way in, and is written out
// exactly as given. Bytes that are not text travel base64-encoded, with
// "bodyEncoding": "base64" saying so.
//
// The decoding is strict on purpose. An ordinary business output such as
// {"ok": true} must not pass for an empty page, so an unknown field — a known
// one spelled with other capitals included — is a refusal, and so is anything net/http would silently drop or mangle: a header
// we compute, a header that would split the response, two spellings of one
// header whose winner a map's iteration order would pick.

// errResponseRefused marks a stdout that ran to completion and is not a response
// this server will write.
//
// reason is the rule that was broken, worded for the author of the function,
// and it is all Error() says: it travels in a public 502, and nothing printed by
// the function is ours to publish there — not even a field or header name, which
// is a fragment of stdout like any other. subject is that name, when there is
// one, and it goes to the server log alone, where the author looks.
type errResponseRefused struct {
	reason  string
	subject string
}

func (e *errResponseRefused) Error() string {
	return "the function did not answer with a valid response envelope: " + e.reason
}

func refuseResponse(reason string) error {
	return &errResponseRefused{reason: reason}
}

// refuseResponseAbout is refuseResponse naming, for the server log only, the
// field or header the refusal is about.
func refuseResponseAbout(subject, reason string) error {
	return &errResponseRefused{reason: reason, subject: subject}
}

// reservedResponseHeaders are the headers a function may not set. The first is
// computed from the body; the others describe the connection rather than the
// response, and a value that disagreed with what net/http actually does would
// corrupt the framing for whatever sits between us and the browser.
var reservedResponseHeaders = map[string]bool{
	"content-length":    true,
	"transfer-encoding": true,
	"connection":        true,
	"keep-alive":        true,
	"upgrade":           true,
	"te":                true,
	"trailer":           true,
}

// defaultResponseContentType is what a response carries when its envelope names
// no content-type. Plain text, not HTML: a function that forgot the header must
// not have a browser interpret what it wrote.
const defaultResponseContentType = "text/plain; charset=utf-8"

// pageResponse is a decoded envelope, ready to be written.
type pageResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// envelopeFields are the names an envelope may carry, spelled exactly.
//
// The envelope is read as a map of exact names, and not into a tagged struct:
// encoding/json matches a struct field without regard to case, so a struct
// would take {"Body": ...} for a body — a strict decoder accepting a spelling
// the contract does not have — and would let {"body": "a", "Body": "b"} pick its
// winner by position in the document.
var envelopeFields = map[string]bool{
	"status":       true,
	"headers":      true,
	"body":         true,
	"bodyEncoding": true,
}

// decodeResponseEnvelope turns a function's stdout into the response to write,
// or refuses it.
//
// A truncated stdout is refused whatever its shape, before it is even read: a
// page cut at the capture cap is never served, even in the unlikely case that
// what survived still parses.
//
// Every field is decoded into a pointer, or a map of pointers for the headers,
// which tells an absent or null field apart from an empty one: a body or a
// header value that is not a string has to be refused, not read as "".
func decodeResponseEnvelope(stdout string, truncated bool) (*pageResponse, error) {
	if truncated {
		return nil, &errOutputUnusable{limit: maxOutputSize}
	}

	trimmed := bytes.TrimSpace([]byte(stdout))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, refuseResponse("stdout must be one JSON object")
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil {
		return nil, refuseResponse("stdout must be one JSON object")
	}
	// One object and nothing after it: a second line printed by mistake would
	// otherwise be dropped without a word.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, refuseResponse("stdout must hold one JSON object and nothing else")
	}

	// Sorted, so that of two unknown fields the same one is named every time.
	// The name came out of stdout: it goes to the log, not into the reason.
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if !envelopeFields[name] {
			return nil, refuseResponseAbout(name,
				"unknown field; the envelope takes status, headers, body and bodyEncoding, spelled exactly so, and nothing else")
		}
	}

	var bodyText *string
	if err := decodeField(fields, "body", &bodyText); err != nil || bodyText == nil {
		return nil, refuseResponse(`"body" is required and must be a string`)
	}

	var statusCode *int
	if err := decodeField(fields, "status", &statusCode); err != nil {
		return nil, refuseResponse(`"status" must be an integer from 200 to 599`)
	}
	status := http.StatusOK
	if statusCode != nil {
		status = *statusCode
		if status < 200 || status > 599 {
			return nil, refuseResponse(`"status" must be an integer from 200 to 599`)
		}
	}

	var encoding *string
	if err := decodeField(fields, "bodyEncoding", &encoding); err != nil {
		return nil, refuseResponse(`"bodyEncoding" must be absent or "base64"`)
	}
	body := []byte(*bodyText)
	if encoding != nil {
		if *encoding != "base64" {
			return nil, refuseResponse(`"bodyEncoding" must be absent or "base64"`)
		}
		decoded, err := base64.StdEncoding.DecodeString(*bodyText)
		if err != nil {
			return nil, refuseResponse(`"body" is not valid base64`)
		}
		body = decoded
	}

	var wireHeaders map[string]*string
	if err := decodeField(fields, "headers", &wireHeaders); err != nil {
		return nil, refuseResponse(`"headers" must be a flat object of strings`)
	}
	headers, err := responseHeaders(wireHeaders)
	if err != nil {
		return nil, err
	}

	return &pageResponse{Status: status, Headers: headers, Body: body}, nil
}

// decodeField decodes one field of the envelope into target, and leaves target
// untouched when the field is absent.
func decodeField(fields map[string]json.RawMessage, name string, target any) error {
	raw, ok := fields[name]
	if !ok {
		return nil
	}
	return json.Unmarshal(raw, target)
}

// responseHeaders weighs the headers of an envelope and lowercases their names.
func responseHeaders(wire map[string]*string) (map[string]string, error) {
	headers := make(map[string]string, len(wire))
	for name, value := range wire {
		if !validHeaderName(name) {
			return nil, refuseResponseAbout(name, "a header name is not a valid HTTP token")
		}
		lower := strings.ToLower(name)
		if reservedResponseHeaders[lower] {
			// Named in the reason: the list is ours, not the function's.
			return nil, refuseResponse(fmt.Sprintf(
				"header %q is set by the server and cannot be set by a function", lower))
		}
		if value == nil {
			return nil, refuseResponseAbout(name, `"headers" must be a flat object of strings`)
		}
		if strings.ContainsAny(*value, "\r\n\x00") {
			return nil, refuseResponseAbout(name, "a header value contains CR, LF or NUL")
		}
		if _, seen := headers[lower]; seen {
			return nil, refuseResponseAbout(lower, "a header is given twice, under two spellings")
		}
		headers[lower] = *value
	}
	return headers, nil
}

// validHeaderName reports whether name is an HTTP token (RFC 9110 §5.6.2).
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// writePageResponse writes a decoded envelope.
//
// Content-Length is ours, computed from the body. A status that carries no body
// (204, 304) gets none: net/http refuses the write, and what the function put in
// "body" is dropped rather than refused — the envelope said nothing wrong, the
// protocol has no place for it.
func writePageResponse(w http.ResponseWriter, page *pageResponse) error {
	header := w.Header()
	for name, value := range page.Headers {
		header.Set(name, value)
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", defaultResponseContentType)
	}
	if bodyAllowed(page.Status) {
		header.Set("Content-Length", fmt.Sprint(len(page.Body)))
	}

	w.WriteHeader(page.Status)
	if !bodyAllowed(page.Status) {
		return nil
	}
	_, err := w.Write(page.Body)
	return err
}

// bodyAllowed mirrors net/http's own rule for the statuses an envelope can hold.
func bodyAllowed(status int) bool {
	return status != http.StatusNoContent && status != http.StatusNotModified
}
