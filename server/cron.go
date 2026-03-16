package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/cron"
)

// cronQueueDepth tracks the number of in-flight (waiting + running) cron executions per function.
var cronQueueDepth sync.Map // map[string]*atomic.Int32

const (
	faasboxCronJobsCollection = "faasbox_cron_jobs"
	cronJobPrefix             = "__faasboxCron_"
)

// ensureCronJobsCollection creates the faasbox_cron_jobs collection if it doesn't exist,
// or migrates it by adding missing fields (maxQueue).
func ensureCronJobsCollection(app core.App) error {
	col, err := app.FindCollectionByNameOrId(faasboxCronJobsCollection)
	if err != nil {
		// Collection doesn't exist — create it with all fields
		col = core.NewBaseCollection(faasboxCronJobsCollection)
		col.Fields.Add(
			&core.TextField{Name: "name", Required: true},
			&core.TextField{Name: "schedule", Required: true},
			&core.TextField{Name: "functionName", Required: true},
			&core.JSONField{Name: "payload"},
			&core.BoolField{Name: "active"},
			&core.NumberField{Name: "maxQueue"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		return app.Save(col)
	}

	// Collection exists — add missing fields if needed
	if col.Fields.GetByName("maxQueue") == nil {
		col.Fields.Add(&core.NumberField{Name: "maxQueue"})
		return app.Save(col)
	}
	return nil
}

// validateCronScheduleHook rejects cron job records with an invalid schedule expression.
func validateCronScheduleHook(e *core.RecordEvent) error {
	schedule := e.Record.GetString("schedule")
	if schedule != "" {
		if _, err := cron.NewSchedule(schedule); err != nil {
			return fmt.Errorf("invalid cron expression %q: %w", schedule, err)
		}
	}
	return e.Next()
}

// syncAllCronJobs removes all FaaS cron jobs and re-registers active ones.
// The provided context is passed to each cron execution so that in-flight
// functions can be cancelled when the server shuts down.
func syncAllCronJobs(app core.App, functionsDir string, ctx context.Context) {
	// Remove all existing FaaS crons
	for _, job := range app.Cron().Jobs() {
		id := job.Id()
		if len(id) > len(cronJobPrefix) && id[:len(cronJobPrefix)] == cronJobPrefix {
			app.Cron().Remove(id)
		}
	}

	// Load active cron records
	records, err := app.FindAllRecords(faasboxCronJobsCollection)
	if err != nil {
		app.Logger().Error("faasbox: failed to load cron jobs", "error", err)
		return
	}

	for _, record := range records {
		if !record.GetBool("active") {
			continue
		}

		name := record.GetString("functionName")
		schedule := record.GetString("schedule")
		payload := record.GetString("payload")
		maxQueue := int(record.GetFloat("maxQueue"))

		if name == "" || schedule == "" {
			continue
		}
		if !validName.MatchString(name) || len(name) > 64 {
			app.Logger().Error("faasbox: invalid function name in cron job, skipping",
				"recordId", record.Id, "functionName", name)
			continue
		}

		jobId := cronJobPrefix + record.Id
		err := app.Cron().Add(jobId, schedule, func() {
			runFunction(ctx, app, functionsDir, name, payload, maxQueue)
		})
		if err != nil {
			app.Logger().Error("faasbox: failed to register cron",
				"jobId", jobId, "schedule", schedule, "function", name, "error", err)
		}
	}
}

// runFunction executes a function outside of an HTTP context (for cron jobs).
// maxQueue limits how many executions (waiting + running) can exist simultaneously
// for this function. 0 means no limit.
func runFunction(ctx context.Context, app core.App, functionsDir, name, payload string, maxQueue int) {
	// Check queue depth before blocking on the semaphore
	if maxQueue > 0 {
		val, _ := cronQueueDepth.LoadOrStore(name, &atomic.Int32{})
		counter := val.(*atomic.Int32)
		depth := counter.Add(1)
		defer counter.Add(-1)

		if int(depth) > maxQueue {
			app.Logger().Warn("faasbox cron: queue full, skipping",
				"function", name, "depth", depth, "maxQueue", maxQueue)
			return
		}
	}

	// Acquire semaphore (block until a slot is available)
	sem <- struct{}{}
	defer func() { <-sem }()

	if payload == "" {
		payload = "{}"
	}

	env := lookupFunctionEnv(app, name)
	res := executeFunction(ctx, functionsDir, name, payload, env)

	// Log to faasbox_logs collection
	status := "success"
	if res.TimedOut {
		status = "timeout"
	} else if res.Err != nil {
		status = "error"
	}
	recordExecution(app, logEntry{
		FunctionName:   name,
		Trigger:        "cron",
		Status:         status,
		DurationMs:     res.Duration.Milliseconds(),
		Stdout:         res.Stdout,
		Stderr:         res.Stderr,
		RequestPayload: payload,
		ExitCode:       res.ExitCode,
	})

	// Log to console as well
	if res.Err != nil {
		app.Logger().Error("faasbox cron: execution failed",
			"function", name, "error", res.Err, "stdout", res.Stdout, "stderr", res.Stderr, "truncated", res.Truncated)
		return
	}

	app.Logger().Info("faasbox cron: executed",
		"function", name, "stdout", res.Stdout, "stderr", res.Stderr, "truncated", res.Truncated)
}
