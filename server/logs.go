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
// Raising them only takes effect on a database whose faasbox_logs collection
// does not exist yet: ensureLogsCollection derives the declared field size
// from these values and never revisits an existing collection. Raising the
// output cap on an established database would let recordExecution build a
// value the declared Max rejects, and the record would be dropped. Reset the
// database after changing them (cf. AGENTS.md, "Stade du projet").
var (
	maxLoggedOutput  = envInt("FAASBOX_MAX_LOG_OUTPUT", defaultMaxLoggedOutput)
	maxLoggedPayload = envInt("FAASBOX_MAX_LOG_PAYLOAD", defaultMaxLoggedPayload)
)

// ensureLogsCollection creates the faasbox_logs collection if it doesn't exist.
//
// It carries both a relation and a name, and that denormalisation is deliberate.
// The relation is what filtering and cascade deletion need — a log follows its
// function through a rename, and goes away with it. The name is a trace: a log
// says what the function was called *at that moment*, which is the only reading
// that makes an old entry mean anything after a rename.
//
// It requires faasbox_functions to exist already: OnServe creates it first.
func ensureLogsCollection(app core.App) error {
	if _, err := app.FindCollectionByNameOrId(faasboxLogsCollection); err == nil {
		return nil
	}

	functions, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		return fmt.Errorf("collection %s not found: %w", faasboxFunctionsCollection, err)
	}

	col := core.NewBaseCollection(faasboxLogsCollection)

	col.Fields.Add(
		&core.RelationField{
			Name:          "function",
			MaxSelect:     1,
			CascadeDelete: true,
			CollectionId:  functions.Id,
		},
		&core.TextField{Name: "functionName", Required: true},
		&core.SelectField{Name: "trigger", Required: true, Values: []string{"http", "cron"}},
		&core.SelectField{Name: "status", Required: true, Values: []string{"success", "error", "timeout", "missed"}},
		&core.NumberField{Name: "duration"},
		&core.TextField{Name: "stdout", Max: maxLoggedOutput + logMarkerSlack},
		&core.TextField{Name: "stderr", Max: maxLoggedOutput + logMarkerSlack},
		&core.JSONField{Name: "requestPayload"},
		&core.NumberField{Name: "exitCode"},
		&core.BoolField{Name: "truncated"},
		&core.AutodateField{Name: "created", OnCreate: true},
		&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
	)

	return app.Save(col)
}

type logEntry struct {
	// FunctionId ties the entry to the record; FunctionName says what that
	// record was called when the entry was written. See ensureLogsCollection.
	FunctionId     string
	FunctionName   string
	Trigger        string // "http" or "cron"
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
func recordExecution(app core.App, entry logEntry) {
	col, err := app.FindCollectionByNameOrId(faasboxLogsCollection)
	if err != nil {
		app.Logger().Error("faasbox: cannot find logs collection", "error", err)
		return
	}

	stdout, stdoutCut := truncateForLog(entry.Stdout, maxLoggedOutput)
	stderr, stderrCut := truncateForLog(entry.Stderr, maxLoggedOutput)
	truncated := stdoutCut || stderrCut

	record := core.NewRecord(col)
	record.Set("function", entry.FunctionId)
	record.Set("functionName", entry.FunctionName)
	record.Set("trigger", entry.Trigger)
	record.Set("status", entry.Status)
	record.Set("duration", entry.DurationMs)
	record.Set("stdout", stdout)
	record.Set("stderr", stderr)
	if entry.RequestPayload != "" {
		payload, payloadCut := truncateForLog(entry.RequestPayload, maxLoggedPayload)
		record.Set("requestPayload", payload)
		truncated = truncated || payloadCut
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
