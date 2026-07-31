package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"unicode/utf8"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

const (
	faasboxLogsCollection  = "faasbox_logs"
	defaultMaxLogRetention = 1000
)

// Persisted log entries are bounded far below the execution capture limits
// (10 MB per stream, 1 MB per request body): the full output stays in the
// immediate HTTP response, only the stored copy is capped.
const (
	maxLoggedOutput  = 8 << 10 // 8 KB per captured stream
	maxLoggedPayload = 4 << 10 // 4 KB for the request payload

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
var maxLogRetention = func() int {
	s := os.Getenv("FAASBOX_MAX_LOG_RETENTION")
	if s == "" {
		return defaultMaxLogRetention
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		log.Printf("faasbox: invalid FAASBOX_MAX_LOG_RETENTION=%q, using default %d", s, defaultMaxLogRetention)
		return defaultMaxLogRetention
	}
	return n
}()

// ensureLogsCollection creates the faasbox_logs collection if it doesn't exist.
func ensureLogsCollection(app core.App) error {
	if _, err := app.FindCollectionByNameOrId(faasboxLogsCollection); err == nil {
		return nil
	}

	col := core.NewBaseCollection(faasboxLogsCollection)

	col.Fields.Add(
		&core.TextField{Name: "functionName", Required: true},
		&core.SelectField{Name: "trigger", Required: true, Values: []string{"http", "cron"}},
		&core.SelectField{Name: "status", Required: true, Values: []string{"success", "error", "timeout", "missed"}},
		&core.NumberField{Name: "duration"},
		&core.TextField{Name: "stdout", Max: maxLoggedOutput + logMarkerSlack},
		&core.TextField{Name: "stderr", Max: maxLoggedOutput + logMarkerSlack},
		&core.JSONField{Name: "requestPayload"},
		&core.NumberField{Name: "exitCode"},
		&core.AutodateField{Name: "created", OnCreate: true},
		&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
	)

	return app.Save(col)
}

type logEntry struct {
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

	stdout, _ := truncateForLog(entry.Stdout, maxLoggedOutput)
	stderr, _ := truncateForLog(entry.Stderr, maxLoggedOutput)

	record := core.NewRecord(col)
	record.Set("functionName", entry.FunctionName)
	record.Set("trigger", entry.Trigger)
	record.Set("status", entry.Status)
	record.Set("duration", entry.DurationMs)
	record.Set("stdout", stdout)
	record.Set("stderr", stderr)
	if entry.RequestPayload != "" {
		payload, _ := truncateForLog(entry.RequestPayload, maxLoggedPayload)
		record.Set("requestPayload", payload)
	}
	record.Set("exitCode", entry.ExitCode)

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
