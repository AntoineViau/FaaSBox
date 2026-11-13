package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDecodeResponseEnvelope_Accepts covers what a valid envelope becomes: the
// status and headers it names, the body as given, and the two defaults.
func TestDecodeResponseEnvelope_Accepts(t *testing.T) {
	cases := []struct {
		name        string
		stdout      string
		wantStatus  int
		wantHeaders map[string]string
		wantBody    string
	}{
		{
			name:        "status, headers and body applied",
			stdout:      `{"status":201,"headers":{"Content-Type":"text/html; charset=utf-8","X-Custom":"a"},"body":"<h1>Hi</h1>"}`,
			wantStatus:  201,
			wantHeaders: map[string]string{"content-type": "text/html; charset=utf-8", "x-custom": "a"},
			wantBody:    "<h1>Hi</h1>",
		},
		{
			name:        "status absent reads as 200",
			stdout:      `{"body":"ok"}`,
			wantStatus:  200,
			wantHeaders: map[string]string{},
			wantBody:    "ok",
		},
		{
			name:        "a redirect with an empty body",
			stdout:      `{"status":302,"headers":{"location":"https://example.com"},"body":""}`,
			wantStatus:  302,
			wantHeaders: map[string]string{"location": "https://example.com"},
			wantBody:    "",
		},
		{
			name:        "base64 decodes to bytes",
			stdout:      `{"body":"AAEC/w==","bodyEncoding":"base64","headers":{"content-type":"application/octet-stream"}}`,
			wantStatus:  200,
			wantHeaders: map[string]string{"content-type": "application/octet-stream"},
			wantBody:    "\x00\x01\x02\xff",
		},
		{
			name:        "trailing newline of console.log tolerated",
			stdout:      "{\"body\":\"x\"}\n",
			wantStatus:  200,
			wantHeaders: map[string]string{},
			wantBody:    "x",
		},
		{
			name:        "the bounds of status accepted",
			stdout:      `{"status":599,"body":""}`,
			wantStatus:  599,
			wantHeaders: map[string]string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := decodeResponseEnvelope(tc.stdout, false)
			if err != nil {
				t.Fatalf("decodeResponseEnvelope() refused: %v", err)
			}
			if page.Status != tc.wantStatus {
				t.Errorf("status = %d, want %d", page.Status, tc.wantStatus)
			}
			if len(page.Headers) != len(tc.wantHeaders) {
				t.Errorf("headers = %v, want %v", page.Headers, tc.wantHeaders)
			}
			for name, value := range tc.wantHeaders {
				if page.Headers[name] != value {
					t.Errorf("header %q = %q, want %q", name, page.Headers[name], value)
				}
			}
			if string(page.Body) != tc.wantBody {
				t.Errorf("body = %q, want %q", page.Body, tc.wantBody)
			}
		})
	}
}

// TestDecodeResponseEnvelope_Refuses covers every rule, one stdout each. What is
// asserted is the type — the 502 depends on it — the rule named in the message,
// and the absence of anything the function printed.
func TestDecodeResponseEnvelope_Refuses(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		rule   string
		secret string // a fragment of stdout that must not reach the message
	}{
		{"plain text", "hello sk_live_1", "one JSON object", "sk_live_1"},
		{"a JSON array", `["sk_live_1"]`, "one JSON object", "sk_live_1"},
		{"a JSON string", `"sk_live_1"`, "one JSON object", "sk_live_1"},
		{"empty stdout", "", "one JSON object", ""},
		{"an ordinary business output", `{"ok":true}`, "unknown field", ""},
		{"an unknown field named after a secret", `{"body":"","sk_live_1":1}`, "unknown field", "sk_live_1"},
		{"body absent", `{"status":200}`, `"body" is required`, ""},
		{"body null", `{"body":null}`, `"body" is required`, ""},
		{"body not a string", `{"body":{"a":1}}`, `"body" is required`, ""},
		{"status a string", `{"status":"200","body":""}`, `"status" must be an integer`, ""},
		{"status fractional", `{"status":200.5,"body":""}`, `"status" must be an integer`, ""},
		{"status below 200", `{"status":101,"body":""}`, "from 200 to 599", ""},
		{"status above 599", `{"status":600,"body":""}`, "from 200 to 599", ""},
		{"content-length reserved", `{"headers":{"Content-Length":"3"},"body":""}`, `"content-length" is set by the server`, ""},
		{"transfer-encoding reserved", `{"headers":{"transfer-encoding":"chunked"},"body":""}`, `"transfer-encoding" is set by the server`, ""},
		{"connection reserved", `{"headers":{"Connection":"close"},"body":""}`, `"connection" is set by the server`, ""},
		{"keep-alive reserved", `{"headers":{"keep-alive":"x"},"body":""}`, `"keep-alive" is set by the server`, ""},
		{"upgrade reserved", `{"headers":{"upgrade":"x"},"body":""}`, `"upgrade" is set by the server`, ""},
		{"te reserved", `{"headers":{"TE":"x"},"body":""}`, `"te" is set by the server`, ""},
		{"trailer reserved", `{"headers":{"trailer":"x"},"body":""}`, `"trailer" is set by the server`, ""},
		{"header name with a space", `{"headers":{"x sk_live_1":"v"},"body":""}`, "not a valid HTTP token", "sk_live_1"},
		{"header name empty", `{"headers":{"":"v"},"body":""}`, "not a valid HTTP token", ""},
		{"header name with a colon", `{"headers":{"x:y":"v"},"body":""}`, "not a valid HTTP token", ""},
		{"header value with CR", `{"headers":{"x-a":"a\rsk_live_1"},"body":""}`, "CR, LF or NUL", "sk_live_1"},
		{"header value with LF", `{"headers":{"x-a":"a\nb"},"body":""}`, "CR, LF or NUL", ""},
		{"header value with NUL", `{"headers":{"x-a":"a\u0000b"},"body":""}`, "CR, LF or NUL", ""},
		{"header value not a string", `{"headers":{"x-a":1},"body":""}`, "flat object of strings", ""},
		{"header value null", `{"headers":{"x-a":null},"body":""}`, "flat object of strings", ""},
		{"headers not an object", `{"headers":["a"],"body":""}`, "flat object of strings", ""},
		{"two spellings of one header", `{"headers":{"Location":"/a","location":"/b"},"body":""}`, "given twice", ""},
		{"unknown bodyEncoding", `{"body":"","bodyEncoding":"hex"}`, `"bodyEncoding" must be absent or "base64"`, ""},
		{"bodyEncoding not a string", `{"body":"","bodyEncoding":1}`, `"bodyEncoding" must be absent or "base64"`, ""},
		{"invalid base64", `{"body":"not base64!","bodyEncoding":"base64"}`, "not valid base64", ""},
		{"a second object after the first", `{"body":""}{"body":"sk_live_1"}`, "nothing else", "sk_live_1"},
		{"text after the object", "{\"body\":\"\"}\nsk_live_1", "nothing else", "sk_live_1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeResponseEnvelope(tc.stdout, false)
			var refused *errResponseRefused
			if !errors.As(err, &refused) {
				t.Fatalf("decodeResponseEnvelope(%q) = %v, want *errResponseRefused", tc.stdout, err)
			}
			if !strings.Contains(err.Error(), tc.rule) {
				t.Errorf("message %q does not name the rule %q", err, tc.rule)
			}
			if tc.secret != "" && strings.Contains(err.Error(), tc.secret) {
				t.Errorf("message %q carries a fragment of stdout", err)
			}
		})
	}
}

// TestDecodeResponseEnvelope_TruncatedIsNeverServed covers the capture cap: a
// truncated stdout is refused whatever its shape, even when what survived still
// happens to parse.
func TestDecodeResponseEnvelope_TruncatedIsNeverServed(t *testing.T) {
	for _, stdout := range []string{`{"body":"<h1>Hel`, `{"body":"complete"}`} {
		_, err := decodeResponseEnvelope(stdout, true)
		var unusable *errOutputUnusable
		if !errors.As(err, &unusable) {
			t.Errorf("decodeResponseEnvelope(%q, truncated) = %v, want *errOutputUnusable", stdout, err)
		}
	}
}

// TestWritePageResponse covers what reaches the wire: the body as given with no
// JSON around it, the content-type default, Content-Length computed by us, and a
// bodiless status that drops the body rather than failing.
func TestWritePageResponse(t *testing.T) {
	t.Run("body written verbatim, length computed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		page := &pageResponse{Status: 201, Headers: map[string]string{"content-type": "text/html", "x-a": "b"}, Body: []byte(`<p>"x"</p>`)}
		if err := writePageResponse(rec, page); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 201 {
			t.Errorf("status = %d, want 201", rec.Code)
		}
		if got := rec.Body.String(); got != `<p>"x"</p>` {
			t.Errorf("body = %q, want it verbatim", got)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/html" {
			t.Errorf("content-type = %q, want text/html", got)
		}
		if got := rec.Header().Get("X-A"); got != "b" {
			t.Errorf("x-a = %q, want b", got)
		}
		if got := rec.Header().Get("Content-Length"); got != "10" {
			t.Errorf("content-length = %q, want 10", got)
		}
	})

	t.Run("content-type defaults to plain text", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := writePageResponse(rec, &pageResponse{Status: 200, Headers: map[string]string{}, Body: []byte("<b>")}); err != nil {
			t.Fatal(err)
		}
		if got := rec.Header().Get("Content-Type"); got != defaultResponseContentType {
			t.Errorf("content-type = %q, want %q", got, defaultResponseContentType)
		}
	})

	t.Run("a 204 drops its body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := writePageResponse(rec, &pageResponse{Status: http.StatusNoContent, Headers: map[string]string{}, Body: []byte("x")}); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 204 || rec.Body.Len() != 0 {
			t.Errorf("status %d, body %q; want 204 and nothing", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Length"); got != "" {
			t.Errorf("content-length = %q on a 204, want none", got)
		}
	})
}
