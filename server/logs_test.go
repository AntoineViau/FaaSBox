package main

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pocketbase/dbx"
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

	setupLogsCollection(t, app)

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

	if got := len(decryptedText(app, record, "stdout")); got > maxLoggedOutput+markerSlack {
		t.Errorf("stored stdout is %d bytes, want at most %d plus marker", got, maxLoggedOutput)
	}
	if got := len(decryptedText(app, record, "stderr")); got > maxLoggedOutput+markerSlack {
		t.Errorf("stored stderr is %d bytes, want at most %d plus marker", got, maxLoggedOutput)
	}
	if got := len(decryptedJSON(app, record, "requestPayload")); got > 2*(maxLoggedPayload+markerSlack) {
		// The payload is stored in a JSON field: a truncated payload is no
		// longer valid JSON, so PocketBase quotes and escapes it, which can
		// roughly double its size.
		t.Errorf("stored requestPayload is %d bytes, want at most %d plus marker", got, maxLoggedPayload)
	}

	if !strings.Contains(decryptedText(app, record, "stdout"), "truncated") {
		t.Error("stored stdout has no truncation marker")
	}

	// Fields unrelated to truncation must survive untouched.
	if got := decryptedText(app, record, "functionName"); got != "big-output" {
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

	setupLogsCollection(t, app)

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

	if got := decryptedText(app, records[0], "stdout"); got != `{"ok":true}` {
		t.Errorf("stdout = %q, want it stored verbatim", got)
	}
	if got := decryptedText(app, records[0], "stderr"); got != "debug line" {
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

			setupLogsCollection(t, app)

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

	setupLogsCollection(t, app)

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
	if got := decryptedText(app, records[0], "functionName"); got != "chatty" {
		t.Errorf("filtered record is %q, want %q", got, "chatty")
	}
}

func TestEnsureLogsCollection_TruncatedField(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	setupLogsCollection(t, app)

	col, err := app.FindCollectionByNameOrId(faasboxLogsCollection)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := col.Fields.GetByName("truncated").(*core.BoolField); !ok {
		t.Fatal("truncated field is missing or is not a BoolField")
	}
}

// TestEnsureLogsCollection_TriggerValues pins the closed list of the trigger
// column. A value missing from it does not degrade the entry: PocketBase
// refuses the whole record, so the execution leaves no trace at all — and the
// failure is silent on the caller's side.
func TestEnsureLogsCollection_TriggerValues(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	setupLogsCollection(t, app)

	col, err := app.FindCollectionByNameOrId(faasboxLogsCollection)
	if err != nil {
		t.Fatal(err)
	}
	field, ok := col.Fields.GetByName("trigger").(*core.SelectField)
	if !ok {
		t.Fatalf("trigger is a %T, want a *core.SelectField", col.Fields.GetByName("trigger"))
	}
	for _, want := range []string{"http", "cron", "startup"} {
		if !slices.Contains(field.Values, want) {
			t.Errorf("trigger values = %v, missing %q", field.Values, want)
		}
	}

	// And an entry carrying the new one is actually stored.
	fn := saveTestFunction(t, app, t.TempDir(), "booted", "console.log(1)", "")
	recordExecution(app, logEntry{
		FunctionId:   fn.Id,
		FunctionName: "booted",
		Trigger:      "startup",
		Status:       "success",
	})
	entries := executionLogsOf(t, app, "booted")
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want the single startup run", len(entries))
	}
	if got := entries[0].GetString("trigger"); got != "startup" {
		t.Errorf("trigger = %q, want \"startup\"", got)
	}
}

// TestRecordExecution_TiesTheEntryToTheRecord covers the denormalisation the
// collection carries on purpose: the relation follows the function through a
// rename, the stored name says what it was called when the entry was written.
func TestRecordExecution_TiesTheEntryToTheRecord(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupLogsCollection(t, app)

	fn := saveTestFunction(t, app, t.TempDir(), "before", "console.log(1)", "")
	recordExecution(app, logEntry{
		FunctionId:   fn.Id,
		FunctionName: "before",
		Trigger:      "http",
		Status:       "success",
	})

	fn.Set("name", "after")
	if err := app.Save(fn); err != nil {
		t.Fatal(err)
	}

	entries, err := app.FindAllRecords(faasboxLogsCollection,
		dbx.HashExp{"function": fn.Id})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the rename left %d entries attached to the function, want 1", len(entries))
	}
	if got := decryptedText(app, entries[0], "functionName"); got != "before" {
		t.Errorf("functionName = %q, want the name of the moment, %q", got, "before")
	}
}

// TestDeleteFunction_CascadesToItsLogs pins a deliberate choice: deleting a
// function destroys its history, exactly as the retention purge already does.
func TestDeleteFunction_CascadesToItsLogs(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupLogsCollection(t, app)

	functionsDir := t.TempDir()
	doomed := saveTestFunction(t, app, functionsDir, "doomed", "console.log(1)", "")
	survivor := saveTestFunction(t, app, functionsDir, "survivor", "console.log(1)", "")
	for _, fn := range []*core.Record{doomed, survivor} {
		recordExecution(app, logEntry{
			FunctionId:   fn.Id,
			FunctionName: fn.GetString("name"),
			Trigger:      "http",
			Status:       "success",
		})
	}

	if err := app.Delete(doomed); err != nil {
		t.Fatal(err)
	}

	entries, err := app.FindAllRecords(faasboxLogsCollection)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("faasbox_logs holds %d entries, want only the survivor's", len(entries))
	}
	if got := entries[0].GetString("function"); got != survivor.Id {
		t.Errorf("the surviving entry belongs to %q, want %q", got, survivor.Id)
	}
}

// withMaxLoggedOutput swaps the package-level setting for the duration of the
// test, which is what restarting the server with another FAASBOX_MAX_LOG_OUTPUT
// does.
func withMaxLoggedOutput(t testing.TB, value int) {
	t.Helper()
	previous := maxLoggedOutput
	t.Cleanup(func() { maxLoggedOutput = previous })
	maxLoggedOutput = value
}

// withMaxLoggedPayload is withMaxLoggedOutput for the other setting, the one
// that sizes requestPayload.
func withMaxLoggedPayload(t testing.TB, value int) {
	t.Helper()
	previous := maxLoggedPayload
	t.Cleanup(func() { maxLoggedPayload = previous })
	maxLoggedPayload = value
}

// logsTextFieldMax reads the declared size of a faasbox_logs text field.
func logsTextFieldMax(t testing.TB, app core.App, name string) int {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(faasboxLogsCollection)
	if err != nil {
		t.Fatal(err)
	}
	field, ok := col.Fields.GetByName(name).(*core.TextField)
	if !ok {
		t.Fatalf("%s field is missing or is not a TextField", name)
	}
	return field.Max
}

// logsJSONFieldMaxSize reads the declared size of a faasbox_logs JSON field.
// It is a separate reader because the unit differs: a JSONField declares bytes,
// a TextField runes.
func logsJSONFieldMaxSize(t testing.TB, app core.App, name string) int64 {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(faasboxLogsCollection)
	if err != nil {
		t.Fatal(err)
	}
	field, ok := col.Fields.GetByName(name).(*core.JSONField)
	if !ok {
		t.Fatalf("%s field is missing or is not a JSONField", name)
	}
	return field.MaxSize
}

// TestEnsureLogsCollection_FollowsARaisedSetting covers the repair: the declared
// size of stdout and stderr is not frozen at the value in force the day the
// collection was created.
func TestEnsureLogsCollection_FollowsARaisedSetting(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	withMaxLoggedOutput(t, 2<<10)
	setupLogsCollection(t, app)

	withMaxLoggedOutput(t, 64<<10)
	if err := ensureLogsCollection(app); err != nil {
		t.Fatalf("replaying ensureLogsCollection failed: %v", err)
	}

	want := cipherMax(64<<10 + logMarkerSlack)
	for _, name := range []string{"stdout", "stderr"} {
		if got := logsTextFieldMax(t, app, name); got != want {
			t.Errorf("%s Max = %d, want %d", name, got, want)
		}
	}
}

// TestEnsureLogsCollection_FollowsALoweredSetting is the same move downwards.
// It was already harmless — the built value shrinks with the cap — but the field
// has no reason to keep claiming a room nothing fills.
func TestEnsureLogsCollection_FollowsALoweredSetting(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	withMaxLoggedOutput(t, 64<<10)
	setupLogsCollection(t, app)

	withMaxLoggedOutput(t, 2<<10)
	if err := ensureLogsCollection(app); err != nil {
		t.Fatalf("replaying ensureLogsCollection failed: %v", err)
	}

	want := cipherMax(2<<10 + logMarkerSlack)
	for _, name := range []string{"stdout", "stderr"} {
		if got := logsTextFieldMax(t, app, name); got != want {
			t.Errorf("%s Max = %d, want %d", name, got, want)
		}
	}
}

// TestEnsureLogsCollection_LoweringSparesStoredRows settles the one question the
// repair raises: shrinking the declared size below rows already stored. It is a
// schema change, not a record write, so PocketBase leaves those rows alone —
// only a later edit of one would fail its validation, and nobody edits a log.
func TestEnsureLogsCollection_LoweringSparesStoredRows(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	withMaxLoggedOutput(t, 64<<10)
	setupLogsCollection(t, app)

	const written = 20 << 10
	recordExecution(app, logEntry{
		FunctionName: "chatty",
		Trigger:      "http",
		Status:       "success",
		Stdout:       strings.Repeat("o", written),
	})

	withMaxLoggedOutput(t, 2<<10)
	if err := ensureLogsCollection(app); err != nil {
		t.Fatalf("lowering the declared size over a stored row failed: %v", err)
	}

	records, err := app.FindAllRecords(faasboxLogsCollection)
	if err != nil {
		t.Fatalf("failed to read back logs: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d log records, want the stored one kept", len(records))
	}
	if got := len(decryptedText(app, records[0], "stdout")); got != written {
		t.Errorf("stored stdout is %d bytes, want the %d written before the change", got, written)
	}
}

// TestEnsureLogsCollection_SavesTheCollectionOnce guards the cost of the repair:
// at steady settings the collection is written when it is created and never
// again, however many times the server restarts. It covers the three resized
// columns at once, being a count of writes to the collection itself.
func TestEnsureLogsCollection_SavesTheCollectionOnce(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	writes := 0
	count := func(e *core.CollectionEvent) error {
		if e.Collection.Name == faasboxLogsCollection {
			writes++
		}
		return e.Next()
	}
	app.OnCollectionAfterCreateSuccess().BindFunc(count)
	app.OnCollectionAfterUpdateSuccess().BindFunc(count)

	// Both settings off their defaults: a sum spelled one way at creation and
	// another at realignment would show up here as a second write, for either
	// of the two units.
	withMaxLoggedOutput(t, 3<<10)
	withMaxLoggedPayload(t, 5<<10)
	setupLogsCollection(t, app)
	for i := range 2 {
		if err := ensureLogsCollection(app); err != nil {
			t.Fatalf("replay %d of ensureLogsCollection failed: %v", i+1, err)
		}
	}

	if writes != 1 {
		t.Errorf("faasbox_logs was written %d times, want only the creation", writes)
	}
}

// TestRecordExecution_SurvivesARaisedSetting reads a raised setting from the far
// end: what matters is that the row exists at all. When the declared size stayed
// behind the setting, the invocation still answered and the log line silently
// disappeared.
func TestRecordExecution_SurvivesARaisedSetting(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	const oldCap = 2 << 10
	withMaxLoggedOutput(t, oldCap)
	setupLogsCollection(t, app)

	withMaxLoggedOutput(t, 64<<10)
	if err := ensureLogsCollection(app); err != nil {
		t.Fatalf("replaying ensureLogsCollection failed: %v", err)
	}

	// Past the old cap, under the new one: stored whole, and stored at all.
	const written = 20 << 10
	recordExecution(app, logEntry{
		FunctionName: "chatty",
		Trigger:      "http",
		Status:       "success",
		Stdout:       strings.Repeat("o", written),
	})

	records, err := app.FindAllRecords(faasboxLogsCollection)
	if err != nil {
		t.Fatalf("failed to read back logs: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d log records, want 1 — the raised cap dropped the row", len(records))
	}
	if got := len(decryptedText(app, records[0], "stdout")); got != written {
		t.Errorf("stored stdout is %d bytes, want the %d written", got, written)
	}
	if records[0].GetBool("truncated") {
		t.Error("truncated = true, want false: the output fits under the raised cap")
	}
}

// TestEnsureLogsCollection_FollowsARaisedPayloadSetting is the stdout story for
// requestPayload: declared from the setting at creation, and widened at the next
// start when the setting goes up. Left at the PocketBase default, the column
// capped the payload at 1 MB whatever FAASBOX_MAX_LOG_PAYLOAD said.
func TestEnsureLogsCollection_FollowsARaisedPayloadSetting(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	withMaxLoggedPayload(t, 2<<10)
	setupLogsCollection(t, app)

	if got, want := logsJSONFieldMaxSize(t, app, "requestPayload"), int64(cipherMaxBytes(2<<10+logMarkerSlack)); got != want {
		t.Errorf("requestPayload MaxSize at creation = %d, want %d", got, want)
	}

	withMaxLoggedPayload(t, 64<<10)
	if err := ensureLogsCollection(app); err != nil {
		t.Fatalf("replaying ensureLogsCollection failed: %v", err)
	}

	if got, want := logsJSONFieldMaxSize(t, app, "requestPayload"), int64(cipherMaxBytes(64<<10+logMarkerSlack)); got != want {
		t.Errorf("requestPayload MaxSize = %d, want %d", got, want)
	}
}

// TestRecordExecution_SurvivesARaisedPayloadSetting reads the same repair from
// the far end, and crosses the threshold that makes it matter: an undeclared
// JSONField caps at PocketBase's 1 MB default, so a setting raised past that is
// where the column and the setting part company. Without the realignment
// PocketBase refuses the record, and the invocation answers as if nothing
// happened while the log line is simply not there.
func TestRecordExecution_SurvivesARaisedPayloadSetting(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	setupLogsCollection(t, app)

	// Past the 1 MB the column would otherwise be held to, and past what the
	// sealed value costs on top of the plaintext.
	const raised = 2 << 20
	withMaxLoggedPayload(t, raised)
	if err := ensureLogsCollection(app); err != nil {
		t.Fatalf("replaying ensureLogsCollection failed: %v", err)
	}

	// Exactly at the new bound, so it is stored whole: truncateForLog leaves a
	// value of the cap's own size alone.
	payload := `{"p":"` + strings.Repeat("x", raised-len(`{"p":""}`)) + `"}`
	recordExecution(app, logEntry{
		FunctionName:   "verbose-caller",
		Trigger:        "http",
		Status:         "success",
		RequestPayload: payload,
	})

	records, err := app.FindAllRecords(faasboxLogsCollection)
	if err != nil {
		t.Fatalf("failed to read back logs: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d log records, want 1 — the raised cap dropped the row", len(records))
	}
	if got := decryptedJSON(app, records[0], "requestPayload"); got != payload {
		t.Errorf("stored requestPayload is %d bytes, want the %d written", len(got), len(payload))
	}
	if records[0].GetBool("truncated") {
		t.Error("truncated = true, want false: the payload fits under the raised cap")
	}
}

func TestEnsureLogsCollection_StatusValues(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	setupLogsCollection(t, app)

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
