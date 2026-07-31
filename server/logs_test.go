package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestTruncateForLog_UnderLimit(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
	}{
		{"Empty", "", 16},
		{"Short", "hello", 16},
		{"Exactly at limit", strings.Repeat("a", 16), 16},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, truncated := truncateForLog(tt.in, tt.max)
			if truncated {
				t.Error("truncateForLog() reported a truncation under the limit")
			}
			if got != tt.in {
				t.Errorf("truncateForLog() = %q, want the input unchanged", got)
			}
		})
	}
}

func TestTruncateForLog_OverLimit(t *testing.T) {
	in := strings.Repeat("a", 100)

	got, truncated := truncateForLog(in, 10)
	if !truncated {
		t.Fatal("truncateForLog() did not report a truncation over the limit")
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 10)) {
		t.Errorf("truncateForLog() = %q, want the first 10 bytes kept", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("truncateForLog() = %q, want a truncation marker", got)
	}
	// The marker must state the original size, not the truncated one.
	if !strings.Contains(got, "100 bytes") {
		t.Errorf("truncateForLog() = %q, want the original size (100 bytes) in the marker", got)
	}
}

func TestTruncateForLog_RuneBoundary(t *testing.T) {
	// "é" is 2 bytes, "🚀" is 4 bytes: cutting at an arbitrary byte offset
	// lands in the middle of a rune for most limits.
	in := strings.Repeat("é🚀", 100)

	for max := 1; max <= 32; max++ {
		got, truncated := truncateForLog(in, max)
		if !truncated {
			t.Fatalf("max=%d: truncateForLog() did not report a truncation", max)
		}
		if !utf8.ValidString(got) {
			t.Errorf("max=%d: truncateForLog() returned invalid UTF-8: %q", max, got)
		}
		if len(got) > max+len("\n...[truncated, 600 bytes total]") {
			t.Errorf("max=%d: truncateForLog() returned %d bytes, beyond limit plus marker", max, len(got))
		}
	}
}

func TestRecordExecution_TruncatesPersistedFields(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	if err := ensureLogsCollection(app); err != nil {
		t.Fatalf("failed to create logs collection: %v", err)
	}

	const hugeOutput = 5 << 20 // 5 MB, the size an unbounded run can reach
	entry := logEntry{
		FunctionName:   "big-output",
		Trigger:        "http",
		Status:         "success",
		DurationMs:     42,
		Stdout:         strings.Repeat("o", hugeOutput),
		Stderr:         strings.Repeat("e", hugeOutput),
		RequestPayload: `{"data":"` + strings.Repeat("p", 1<<20) + `"}`,
		ExitCode:       0,
	}
	recordExecution(app, entry)

	records, err := app.FindAllRecords(faasboxLogsCollection)
	if err != nil {
		t.Fatalf("failed to read back logs: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d log records, want 1", len(records))
	}
	record := records[0]

	// Marker length is bounded by the size it reports; a generous slack keeps
	// the assertion about the cap, not about the exact marker wording.
	const markerSlack = 64

	if got := len(record.GetString("stdout")); got > maxLoggedOutput+markerSlack {
		t.Errorf("stored stdout is %d bytes, want at most %d plus marker", got, maxLoggedOutput)
	}
	if got := len(record.GetString("stderr")); got > maxLoggedOutput+markerSlack {
		t.Errorf("stored stderr is %d bytes, want at most %d plus marker", got, maxLoggedOutput)
	}
	if got := len(record.GetString("requestPayload")); got > 2*(maxLoggedPayload+markerSlack) {
		// The payload is stored in a JSON field: a truncated payload is no
		// longer valid JSON, so PocketBase quotes and escapes it, which can
		// roughly double its size.
		t.Errorf("stored requestPayload is %d bytes, want at most %d plus marker", got, maxLoggedPayload)
	}

	if !strings.Contains(record.GetString("stdout"), "truncated") {
		t.Error("stored stdout has no truncation marker")
	}

	// Fields unrelated to truncation must survive untouched.
	if got := record.GetString("functionName"); got != "big-output" {
		t.Errorf("functionName = %q, want %q", got, "big-output")
	}
	if got := record.GetInt("duration"); got != 42 {
		t.Errorf("duration = %d, want 42", got)
	}
}

func TestRecordExecution_ShortOutputUntouched(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	if err := ensureLogsCollection(app); err != nil {
		t.Fatalf("failed to create logs collection: %v", err)
	}

	recordExecution(app, logEntry{
		FunctionName: "small-output",
		Trigger:      "cron",
		Status:       "success",
		Stdout:       `{"ok":true}`,
		Stderr:       "debug line",
	})

	records, err := app.FindAllRecords(faasboxLogsCollection)
	if err != nil {
		t.Fatalf("failed to read back logs: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d log records, want 1", len(records))
	}

	if got := records[0].GetString("stdout"); got != `{"ok":true}` {
		t.Errorf("stdout = %q, want it stored verbatim", got)
	}
	if got := records[0].GetString("stderr"); got != "debug line" {
		t.Errorf("stderr = %q, want it stored verbatim", got)
	}
}

// TestRecordExecution_TruncatedFlag covers the persisted flag: it is true as
// soon as any one of the three stored fields was cut, false otherwise. It
// reports a cut made at storage time, which is not the same event as the
// capture cap reached during the run.
func TestRecordExecution_TruncatedFlag(t *testing.T) {
	over := strings.Repeat("x", maxLoggedOutput+1)
	overPayload := `{"data":"` + strings.Repeat("p", maxLoggedPayload) + `"}`

	cases := []struct {
		name  string
		entry logEntry
		want  bool
	}{
		{
			name:  "stdout cut",
			entry: logEntry{FunctionName: "f", Trigger: "http", Status: "success", Stdout: over},
			want:  true,
		},
		{
			name:  "stderr cut",
			entry: logEntry{FunctionName: "f", Trigger: "http", Status: "success", Stderr: over},
			want:  true,
		},
		{
			name:  "payload cut",
			entry: logEntry{FunctionName: "f", Trigger: "http", Status: "success", RequestPayload: overPayload},
			want:  true,
		},
		{
			name: "nothing cut",
			entry: logEntry{
				FunctionName:   "f",
				Trigger:        "http",
				Status:         "success",
				Stdout:         `{"ok":true}`,
				Stderr:         "debug line",
				RequestPayload: `{"id":1}`,
			},
			want: false,
		},
		{
			name:  "empty entry",
			entry: logEntry{FunctionName: "f", Trigger: "cron", Status: "success"},
			want:  false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			app, err := tests.NewTestApp()
			if err != nil {
				t.Fatal(err)
			}
			defer app.Cleanup()

			if err := ensureLogsCollection(app); err != nil {
				t.Fatalf("failed to create logs collection: %v", err)
			}

			recordExecution(app, tt.entry)

			records, err := app.FindAllRecords(faasboxLogsCollection)
			if err != nil {
				t.Fatalf("failed to read back logs: %v", err)
			}
			if len(records) != 1 {
				t.Fatalf("got %d log records, want 1", len(records))
			}
			if got := records[0].GetBool("truncated"); got != tt.want {
				t.Errorf("truncated = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRecordExecution_TruncatedIsFilterable locks what the flag is for: picking
// the incomplete records out of the collection. A marker buried in the text of
// a field cannot do that.
func TestRecordExecution_TruncatedIsFilterable(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	if err := ensureLogsCollection(app); err != nil {
		t.Fatalf("failed to create logs collection: %v", err)
	}

	recordExecution(app, logEntry{
		FunctionName: "chatty",
		Trigger:      "http",
		Status:       "success",
		Stdout:       strings.Repeat("x", maxLoggedOutput+1),
	})
	recordExecution(app, logEntry{
		FunctionName: "quiet",
		Trigger:      "http",
		Status:       "success",
		Stdout:       `{"ok":true}`,
	})

	records, err := app.FindRecordsByFilter(faasboxLogsCollection, "truncated = true", "", 0, 0)
	if err != nil {
		t.Fatalf("failed to filter on truncated: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records matching truncated = true, want 1", len(records))
	}
	if got := records[0].GetString("functionName"); got != "chatty" {
		t.Errorf("filtered record is %q, want %q", got, "chatty")
	}
}

func TestEnsureLogsCollection_TruncatedField(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	if err := ensureLogsCollection(app); err != nil {
		t.Fatal(err)
	}

	col, err := app.FindCollectionByNameOrId(faasboxLogsCollection)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := col.Fields.GetByName("truncated").(*core.BoolField); !ok {
		t.Fatal("truncated field is missing or is not a BoolField")
	}
}

func TestEnsureLogsCollection_StatusValues(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	if err := ensureLogsCollection(app); err != nil {
		t.Fatal(err)
	}

	col, err := app.FindCollectionByNameOrId(faasboxLogsCollection)
	if err != nil {
		t.Fatal(err)
	}

	field, ok := col.Fields.GetByName("status").(*core.SelectField)
	if !ok {
		t.Fatal("status field is not a SelectField")
	}

	want := []string{"success", "error", "timeout", "missed"}
	if len(field.Values) != len(want) {
		t.Fatalf("status values = %v, want %v", field.Values, want)
	}
	for i, v := range want {
		if field.Values[i] != v {
			t.Errorf("status values = %v, want %v", field.Values, want)
			break
		}
	}
}
