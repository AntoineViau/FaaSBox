package main

import (
	"log"
	"os"
	"strconv"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

const (
	faasboxLogsCollection  = "faasbox_logs"
	defaultMaxLogRetention = 1000
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
		&core.SelectField{Name: "status", Required: true, Values: []string{"success", "error", "timeout"}},
		&core.NumberField{Name: "duration"},
		&core.TextField{Name: "stdout"},
		&core.TextField{Name: "stderr"},
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
	Status         string // "success", "error", "timeout"
	DurationMs     int64
	Stdout         string
	Stderr         string
	RequestPayload string
	ExitCode       int
}

// recordExecution persists a function execution log to the faasbox_logs collection.
func recordExecution(app core.App, entry logEntry) {
	col, err := app.FindCollectionByNameOrId(faasboxLogsCollection)
	if err != nil {
		app.Logger().Error("faasbox: cannot find logs collection", "error", err)
		return
	}

	record := core.NewRecord(col)
	record.Set("functionName", entry.FunctionName)
	record.Set("trigger", entry.Trigger)
	record.Set("status", entry.Status)
	record.Set("duration", entry.DurationMs)
	record.Set("stdout", entry.Stdout)
	record.Set("stderr", entry.Stderr)
	if entry.RequestPayload != "" {
		record.Set("requestPayload", entry.RequestPayload)
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
