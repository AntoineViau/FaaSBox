package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// What a function receives on stdin.
//
// stdin used to carry the request body and nothing else, so a function knew
// what it had been sent and never how it had been called: no method, no path,
// no headers, no trigger. A signed webhook was therefore unverifiable — Stripe,
// GitHub and Slack all sign in a header and none of them can be asked to move
// that signature into the body — and a function wired on two triggers could not
// tell which one had woken it.
//
// stdin now carries an **envelope** describing the call, and its shape follows
// the trigger: an HTTP call knows its method, its path, its query and its
// headers; a scheduled run knows the trigger that fired it; an invocation an
// agent asked for has no request in sight and invents nothing to fill the gap.
// What the three share is "trigger" and "body".
//
// **"body" is always a string, carrying the body exactly as received**, and
// that rule is the reason the rest holds. An HMAC is computed over the bytes
// that arrived, so a body the server had parsed and re-serialised would fail
// every signature check ever written against it. It is also what lets a
// function be handed text, XML or a form-encoded payload. Whoever expects JSON
// parses it themselves, one line further down.
//
// The envelope is not counted against FAASBOX_MAX_BODY_SIZE: that setting
// bounds what a caller sends, and the headers around it are already bounded by
// the HTTP server itself.

// The four values a log entry's trigger column may hold. They are the vocabulary
// of the envelope as much as of the log — what feeds a function and what records
// the run are named from the same place, so a path cannot report one and feed
// the shape of another.
const (
	triggerHTTP    = "http"
	triggerCron    = "cron"
	triggerStartup = "startup"
	triggerMCP     = "mcp"
)

// repeatedSeparator renders a header or a query parameter given more than once:
// one value, comma-joined, in the order received. That is the semantics HTTP
// itself gives a repeated header, and it keeps "headers" and "query" flat
// string-to-string objects rather than making every function handle two shapes.
const repeatedSeparator = ", "

// deniedHeaders never enter an envelope, whatever the casing they arrived in.
// They carry what authenticates the caller, and copying them in would hand the
// author of the function the right to act as whoever called it — then write it
// to faasbox_logs, which persists the envelope. The refusal is hard and is not
// an option: what proves who the caller is, is never forwarded.
//
// proxy-authorization sits beside the three the contract names because it is a
// credential by definition and reaches an origin server in the setups where a
// proxy asks for one. The list is not the rule; the principle is, and a name
// that carries a caller's credentials belongs here.
var deniedHeaders = map[string]bool{
	"x-api-key":           true,
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
}

// executionInput is what one execution is fed: the envelope handed to the
// subprocess on stdin, and the trigger the log entry records. The two are built
// together, by the same constructor, and travel together as far as
// recordExecution — nothing can log one path while feeding the shape of another.
type executionInput struct {
	Trigger  string
	Envelope string
}

// httpEnvelope is what an HTTP invocation hands to the function.
type httpEnvelope struct {
	Trigger string            `json:"trigger"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   map[string]string `json:"query"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// triggerEnvelope is what a scheduled run — cron or startup — hands to the
// function. triggerName is what tells two triggers of the same function apart:
// stdin carried the payload and never the context of the call, so the only way
// to know which schedule had fired was to give them deliberately different
// payloads.
type triggerEnvelope struct {
	Trigger     string `json:"trigger"`
	TriggerName string `json:"triggerName"`
	Body        string `json:"body"`
}

// mcpEnvelope is what invoke_function hands to the function. There is no request
// on this path — no method, no path, no headers — and none is invented.
type mcpEnvelope struct {
	Trigger string `json:"trigger"`
	Body    string `json:"body"`
}

// newHTTPInput builds the envelope of an HTTP invocation.
//
// body is carried over untouched. The transport has already bounded it and
// checked it is valid UTF-8, which is what a JSON envelope needs to carry bytes
// without mangling them; nothing here parses it, and nothing here may.
func newHTTPInput(r *http.Request, body []byte) executionInput {
	return executionInput{
		Trigger: triggerHTTP,
		Envelope: marshalEnvelope(httpEnvelope{
			Trigger: triggerHTTP,
			Method:  r.Method,
			// The path as called, query string excluded: the query has its own
			// field, already split into pairs.
			Path:    r.URL.Path,
			Query:   flattenQuery(r.URL.Query()),
			Headers: envelopeHeaders(r.Header),
			Body:    string(body),
		}),
	}
}

// newTriggerInput builds the envelope of a scheduled run. kind is the trigger's
// own kind — "cron" or "startup" — and is what the log entry records as well.
//
// An absent payload becomes "{}". That normalisation used to live in
// runFunction; it belongs here, beside the rule it serves — "body" is always a
// string, so a trigger carrying no payload sends an empty JSON object rather
// than nothing at all.
func newTriggerInput(kind, name, payload string) executionInput {
	return executionInput{
		Trigger: kind,
		Envelope: marshalEnvelope(triggerEnvelope{
			Trigger:     kind,
			TriggerName: name,
			Body:        defaultedBody(payload),
		}),
	}
}

// newMCPInput builds the envelope of an invocation an agent asked for. The same
// normalisation applies, and for the same reason: an agent that named no payload
// is a caller that sent an empty object.
func newMCPInput(payload string) executionInput {
	return executionInput{
		Trigger:  triggerMCP,
		Envelope: marshalEnvelope(mcpEnvelope{Trigger: triggerMCP, Body: defaultedBody(payload)}),
	}
}

// defaultedBody is the single place the absent payload of a trigger or of an
// agent call reads as an empty JSON object. An HTTP body does not go through it:
// a caller that sent nothing sent an empty string, and saying "{}" on its behalf
// would be inventing a document it never wrote.
func defaultedBody(payload string) string {
	if payload == "" {
		return "{}"
	}
	return payload
}

// flattenQuery renders the query string as a flat object, empty when there is no
// query at all.
func flattenQuery(values url.Values) map[string]string {
	flat := make(map[string]string, len(values))
	for key, list := range values {
		flat[key] = strings.Join(list, repeatedSeparator)
	}
	return flat
}

// envelopeHeaders renders the request headers as a flat object, **lowercased**:
// HTTP declares header names case-insensitive, and a function must not have to
// guess which spelling arrived. The headers that authenticate the caller are
// dropped here, and nowhere else.
func envelopeHeaders(header http.Header) map[string]string {
	flat := make(map[string]string, len(header))
	for key, list := range header {
		name := strings.ToLower(key)
		if deniedHeaders[name] {
			continue
		}
		// Assigned, not merged: net/http canonicalises every name it reads off
		// the wire, so "X-Custom" and "x-custom" have already become one key
		// holding both values by the time we get here. Two keys of this map
		// cannot lowercase to the same name, and a merge branch would be a code
		// path nothing can reach — with a map's iteration order deciding what it
		// produced.
		flat[name] = strings.Join(list, repeatedSeparator)
	}
	return flat
}

// marshalEnvelope renders an envelope as the JSON text stdin carries.
//
// The error is dropped, and that is the one thing in this file worth a word:
// encoding/json only fails on values it cannot represent — channels, functions,
// cycles — and an envelope is a struct of strings and string maps. There is no
// failure being hidden, and threading an impossible error through three call
// sites with no way to act on it would be noise.
func marshalEnvelope(envelope any) string {
	rendered, _ := json.Marshal(envelope)
	return string(rendered)
}
