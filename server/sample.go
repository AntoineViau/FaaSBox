package main

import (
	"bytes"
	"encoding/json"
	"strings"
)

// The sample call of a function, in the two shapes it lives in.
//
// The record holds the headers as one serialised JSON string, because what is
// encrypted at rest is a text column and a JSON column would keep its shape and
// travel to the replica in the clear. That is a constraint of persistence, and
// it is not the contract: on the wire the headers are a list, which is what the
// editor edits and what an agent describes.
//
// Both conversions live here, and only here. A second one anywhere would be a
// second chance for the stored form to drift from what the editor writes — and
// the editor is the reference, since it wrote every sample that exists today.

// sampleHeader is one header of the sample call, as the contract carries it.
//
// One type serves the management contract and the MCP schema, where mcpTrigger
// deliberately mirrors manageTrigger rather than reusing it. What forced the
// copy there was a payload that infers as a base64 string and a sentence to
// write for every field; neither applies to a pair of strings. The jsonschema
// tag is what an agent reads next to the field, and encoding/json ignores it.
type sampleHeader struct {
	Name  string `json:"name" jsonschema:"the header name, as the caller would send it"`
	Value string `json:"value" jsonschema:"the value that header carries"`
}

// serializeSampleHeaders renders the rows the way the column holds them, which
// has to be the way the editor renders them — character for character, or two
// authors of the same sample write two different strings.
//
// The HTML escaping of encoding/json is what would break that: left on, it
// replaces <, > and & with their six-character unicode escapes, where
// JSON.stringify leaves them as they are. A signed webhook is precisely where
// such a value turns up.
//
// Two control characters still escape differently — backspace and form feed,
// which this writes as unicode escapes and JSON.stringify writes as \b and \f —
// and that is left alone. Neither can appear in a header value: RFC 9110 admits
// visible characters, space, horizontal tab and obs-text, and nothing else. Tab
// itself escapes the same way on both sides.
//
// An empty list writes an empty column rather than an empty array: a caller that
// sent no sample leaves the column as it found it, and an empty column already
// means "no sample".
//
// The error is dropped because a slice of two-string structs cannot produce one:
// what makes Encode fail is a channel, a function or a cycle, and none of the
// three can reach here.
func serializeSampleHeaders(rows []sampleHeader) string {
	if len(rows) == 0 {
		return ""
	}

	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(rows); err != nil {
		return ""
	}
	return strings.TrimSuffix(out.String(), "\n")
}

// parseSampleHeaders reads the column back into the rows the contract publishes.
//
// It answers with a list and never with nil: a function without a sample
// publishes an empty array, so an agent reads one shape rather than two.
//
// What will not read as a row is dropped rather than repaired, entry by entry —
// the rule the editor applies, and for its reason: the column is editable from
// the PocketBase admin, so what comes out of it is whatever someone typed there.
// Both fields have to be present and to be strings, which is what the pointers
// are for: a missing name is not a name that is empty, and guessing at half a
// row would publish a header nobody wrote.
func parseSampleHeaders(stored string) []sampleHeader {
	rows := make([]sampleHeader, 0)
	if stored == "" {
		return rows
	}

	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(stored), &entries); err != nil {
		return rows
	}

	for _, entry := range entries {
		var row struct {
			Name  *string `json:"name"`
			Value *string `json:"value"`
		}
		if err := json.Unmarshal(entry, &row); err != nil || row.Name == nil || row.Value == nil {
			continue
		}
		rows = append(rows, sampleHeader{Name: *row.Name, Value: *row.Value})
	}
	return rows
}
