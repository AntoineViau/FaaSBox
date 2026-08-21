package main

import (
	"fmt"
	"unicode/utf8"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

const (
	faasboxLogsCollection  = "faasbox_logs"
	defaultMaxLogRetention = 1000
)

// Persisted log entries are bounded far below the execution capture limits:
// the full output stays in the immediate HTTP response, only the stored copy
// is capped.
const (
	defaultMaxLoggedOutput  = 8 << 10 // 8 KB per captured stream
	defaultMaxLoggedPayload = 4 << 10 // 4 KB for the request payload

	// logMarkerSlack covers the truncation marker appended past the cap.
	// A TextField with no explicit Max defaults to 5000 runes and rejects
	// the whole record beyond it, so the declared field size must leave
	// room for the marker. The caps above are counted in bytes while Max
	// is counted in runes: the mismatch is safe, a byte count always
	// overshoots the rune count it stands for.
	logMarkerSlack = 128
)

// maxLogRetention is the maximum number of logs to keep.
// Configurable via FAASBOX_MAX_LOG_RETENTION environment variable.
var maxLogRetention = envInt("FAASBOX_MAX_LOG_RETENTION", defaultMaxLogRetention)

// maxLoggedOutput and maxLoggedPayload bound what a log record stores.
//
// maxLoggedOutput also sets the declared size of the stdout and stderr fields,
// and ensureLogsCollection realigns them on every start: a raised setting
// widens the stored schema before recordExecution can build a value it would
// reject.
var (
	maxLoggedOutput  = envInt("FAASBOX_MAX_LOG_OUTPUT", defaultMaxLoggedOutput)
	maxLoggedPayload = envInt("FAASBOX_MAX_LOG_PAYLOAD", defaultMaxLoggedPayload)
)

// ensureLogsCollection creates the faasbox_logs collection if it doesn't exist,
// and otherwise makes the declared size of stdout and stderr follow the current
// FAASBOX_MAX_LOG_OUTPUT setting.
//
// It carries both a relation and a name, and that denormalisation is deliberate.
// The relation is what filtering and cascade deletion need — a log follows its
// function through a rename, and goes away with it. The name is a trace: a log
// says what the function was called *at that moment*, which is the only reading
// that makes an old entry mean anything after a rename.
//
// It requires faasbox_functions to exist already: OnServe creates it first.
func ensureLogsCollection(app core.App) error {
	// Creation and realignment read the wanted size from the same place. Two
	// spellings of the same sum would make the comparison below miss and save
	// the collection at every boot.
	//
	// The declared size measures the *sealed* value: these two columns are
	// encrypted at rest, and a field capped on the plaintext would refuse an
	// output the setting says to keep.
	wantedOutputMax := cipherMax(maxLoggedOutput + logMarkerSlack)

	col, err := app.FindCollectionByNameOrId(faasboxLogsCollection)
	if err != nil {
		functions, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
		if err != nil {
			return fmt.Errorf("collection %s not found: %w", faasboxFunctionsCollection, err)
		}

		col = core.NewBaseCollection(faasboxLogsCollection)

		col.Fields.Add(
			&core.RelationField{
				Name:          "function",
				MaxSelect:     1,
				CascadeDelete: true,
				CollectionId:  functions.Id,
			},
			&core.TextField{Name: "functionName", Required: true},
			&core.SelectField{Name: "trigger", Required: true, Values: []string{"http", "cron", "startup"}},
			&core.SelectField{Name: "status", Required: true, Values: []string{"success", "error", "timeout", "missed"}},
			&core.NumberField{Name: "duration"},
			&core.TextField{Name: "stdout", Max: wantedOutputMax},
			&core.TextField{Name: "stderr", Max: wantedOutputMax},
			&core.JSONField{Name: "requestPayload"},
			&core.NumberField{Name: "exitCode"},
			&core.BoolField{Name: "truncated"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)

		return app.Save(col)
	}

	// The collection exists: realign the declared size on the current setting.
	// Left frozen at its creation-day value, a raised FAASBOX_MAX_LOG_OUTPUT
	// would have recordExecution build a value the stored schema rejects, and
	// the whole row would vanish while the invocation reports success.
	//
	// A field that is absent — or is not a TextField — yields nil, false and is
	// skipped: it belongs to a collection this code did not create, and there is
	// nothing here to reconcile.
	needsSave := false
	for _, name := range []string{"stdout", "stderr"} {
		field, ok := col.Fields.GetByName(name).(*core.TextField)
		if !ok || field.Max == wantedOutputMax {
			continue
		}
		field.Max = wantedOutputMax
		needsSave = true
	}
	if needsSave {
		return app.Save(col)
	}
	return nil
}

type logEntry struct {
	// FunctionId ties the entry to the record; FunctionName says what that
	// record was called when the entry was written. See ensureLogsCollection.
	FunctionId     string
	FunctionName   string
	Trigger        string // "http", "cron" or "startup"
	Status         string // "success", "error", "timeout", "missed"
	DurationMs     int64
	Stdout         string
	Stderr         string
	RequestPayload string
	ExitCode       int
}

// truncateForLog caps s at max bytes and appends a marker stating the original
// size. The cut is moved back to a rune boundary so the stored value stays valid
// UTF-8. Returns the value and whether it was truncated.
func truncateForLog(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}

	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return s[:cut] + fmt.Sprintf("\n...[truncated, %d bytes total]", len(s)), true
}

// recordExecution persists a function execution log to the faasbox_logs collection.
//
// The four columns that carry what ran are sealed here rather than by a hook,
// because this is the only writer of the collection. They are also the ones the
// threat model cares about most: an output, a stderr trace or an input payload
// is where a secret ends up when a function prints one.
//
// A value that cannot be sealed drops the whole entry. Writing the row with its
// output in the clear is the one outcome the encryption is here to prevent, and
// a missing log line is loud in a way a legible one is not.
func recordExecution(app core.App, entry logEntry) {
	col, err := app.FindCollectionByNameOrId(faasboxLogsCollection)
	if err != nil {
		app.Logger().Error("faasbox: cannot find logs collection", "error", err)
		return
	}

	stdout, stdoutCut := truncateForLog(entry.Stdout, maxLoggedOutput)
	stderr, stderrCut := truncateForLog(entry.Stderr, maxLoggedOutput)
	truncated := stdoutCut || stderrCut

	payload := ""
	if entry.RequestPayload != "" {
		var payloadCut bool
		payload, payloadCut = truncateForLog(entry.RequestPayload, maxLoggedPayload)
		truncated = truncated || payloadCut
	}

	sealed, err := sealLogValues(entry.FunctionName, stdout, stderr, payload)
	if err != nil {
		app.Logger().Error("faasbox: failed to encrypt an execution log",
			"function", entry.FunctionName, "error", err)
		return
	}

	record := core.NewRecord(col)
	record.Set("function", entry.FunctionId)
	record.Set("functionName", sealed[0])
	record.Set("trigger", entry.Trigger)
	record.Set("status", entry.Status)
	record.Set("duration", entry.DurationMs)
	record.Set("stdout", sealed[1])
	record.Set("stderr", sealed[2])
	if payload != "" {
		record.Set("requestPayload", sealed[3])
	}
	record.Set("exitCode", entry.ExitCode)
	// Reports a cut made when writing this record, not one made while capturing
	// the run. The two are distinct: an output can survive the capture cap whole
	// and still be trimmed here.
	record.Set("truncated", truncated)

	if err := app.Save(record); err != nil {
		app.Logger().Error("faasbox: failed to save execution log", "error", err)
	}
}

// sealLogValues encrypts the four columns of an entry, in the order they are
// given. One failure fails the lot: a half-sealed entry is not a shape any
// reader knows.
//
// The payload goes in as text and comes back as text. PocketBase quotes a plain
// string handed to a JSON field, so the column ends up holding a JSON string —
// which is what logRequestPayload reads back apart from a document.
func sealLogValues(values ...string) ([]string, error) {
	sealed := make([]string, len(values))
	for i, value := range values {
		out, err := encryptField(value)
		if err != nil {
			return nil, err
		}
		sealed[i] = out
	}
	return sealed, nil
}

// pruneOldLogs deletes the oldest logs beyond maxLogRetention using a direct
// SQL query to avoid loading records into memory.
func pruneOldLogs(app core.App) {
	total, err := app.CountRecords(faasboxLogsCollection)
	if err != nil || int(total) <= maxLogRetention {
		return
	}

	toDelete := int(total) - maxLogRetention
	_, err = app.DB().NewQuery(
		"DELETE FROM " + faasboxLogsCollection + " WHERE id IN (" +
			"SELECT id FROM " + faasboxLogsCollection + " ORDER BY created ASC LIMIT {:limit}" +
			")",
	).Bind(dbx.Params{"limit": toDelete}).Execute()
	if err != nil {
		app.Logger().Error("faasbox: failed to prune old logs", "error", err)
	}
}
