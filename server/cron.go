package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/cron"
	"github.com/pocketbase/pocketbase/tools/types"
)

// cronQueueDepth tracks the number of in-flight (waiting + running) cron
// executions per function, keyed on the function id — the depth of a function
// must not reset because someone renamed it mid-flight.
var cronQueueDepth sync.Map // map[string]*atomic.Int32

const (
	faasboxCronJobsCollection = "faasbox_cron_jobs"
	cronJobPrefix             = "__faasboxCron_"
)

// ensureCronJobsCollection creates the faasbox_cron_jobs collection if it doesn't exist,
// or migrates it by adding missing fields (maxQueue, lastRunAt).
//
// The target function is a relation, not a name. A name is editable, so a
// trigger wired on one fired into the void from the moment its function was
// renamed — and nothing said so, since nothing was a reference. CascadeDelete
// makes the other half true as well: deleting a function takes its triggers with
// it, with no cleanup code of ours to forget.
//
// It therefore requires faasbox_functions to exist already: OnServe creates that
// collection first.
func ensureCronJobsCollection(app core.App) error {
	col, err := app.FindCollectionByNameOrId(faasboxCronJobsCollection)
	if err != nil {
		functions, ferr := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
		if ferr != nil {
			return fmt.Errorf("collection %s not found: %w", faasboxFunctionsCollection, ferr)
		}

		// Collection doesn't exist — create it with all fields
		col = core.NewBaseCollection(faasboxCronJobsCollection)
		col.Fields.Add(
			&core.TextField{Name: "name", Required: true},
			&core.TextField{Name: "schedule", Required: true},
			&core.RelationField{
				Name:          "function",
				Required:      true,
				MaxSelect:     1,
				CascadeDelete: true,
				CollectionId:  functions.Id,
			},
			&core.JSONField{Name: "payload"},
			&core.BoolField{Name: "active"},
			&core.NumberField{Name: "maxQueue"},
			&core.DateField{Name: "lastRunAt"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		return app.Save(col)
	}

	// Collection exists — add every missing field in a single save
	missing := false
	if col.Fields.GetByName("maxQueue") == nil {
		col.Fields.Add(&core.NumberField{Name: "maxQueue"})
		missing = true
	}
	if col.Fields.GetByName("lastRunAt") == nil {
		col.Fields.Add(&core.DateField{Name: "lastRunAt"})
		missing = true
	}
	if missing {
		return app.Save(col)
	}
	return nil
}

// validateCronScheduleHook rejects cron job records with an invalid schedule expression.
//
// The refusal is an ApiError and not an ordinary error on purpose: the record
// endpoints wrap a hook failure through firstApiError, which keeps the first
// argument that already is an ApiError and discards anything else. An ordinary
// error is replaced by a bare "Failed to create record", and the client is left
// with nothing to show. The message is written for the user — the cron library
// error talks about internal field bounds and belongs in the server log, not in
// the response.
func validateCronScheduleHook(e *core.RecordEvent) error {
	schedule := e.Record.GetString("schedule")
	if schedule != "" {
		if _, err := cron.NewSchedule(schedule); err != nil {
			e.App.Logger().Debug("faasbox cron: rejected schedule",
				"schedule", schedule, "error", err)
			return apis.NewBadRequestError(fmt.Sprintf(
				"Invalid cron expression %q. Expected 5 fields: minute hour day-of-month month day-of-week.",
				schedule,
			), nil)
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

		functionId := record.GetString("function")
		schedule := record.GetString("schedule")
		payload := record.GetString("payload")
		maxQueue := int(record.GetFloat("maxQueue"))

		if functionId == "" || schedule == "" {
			continue
		}
		// Resolved here to refuse a dangling relation at registration time rather
		// than at every tick, and to name the function in the messages below.
		// runFunction resolves again when it fires: the name and the secrets are
		// read then, so a rename in between is picked up without a resync.
		fn, err := app.FindRecordById(faasboxFunctionsCollection, functionId)
		if err != nil {
			app.Logger().Error("faasbox: cron job points at no function, skipping",
				"recordId", record.Id, "functionId", functionId, "error", err)
			continue
		}

		jobId := cronJobPrefix + record.Id
		recordId := record.Id
		err = app.Cron().Add(jobId, schedule, func() {
			runFunction(ctx, app, functionsDir, functionId, payload, maxQueue, recordId)
		})
		if err != nil {
			app.Logger().Error("faasbox: failed to register cron",
				"jobId", jobId, "schedule", schedule, "function", fn.GetString("name"), "error", err)
		}
	}
}

// runFunction executes a function outside of an HTTP context (for cron jobs).
// maxQueue limits how many executions (waiting + running) can exist simultaneously
// for this function. 0 means no limit. recordId identifies the faasbox_cron_jobs
// record whose lastRunAt is stamped once the execution is over; an empty value
// skips the stamping.
//
// The function is resolved here, at fire time, and not captured when the job was
// registered: the scheduler is only rebuilt when a cron record changes, so a name
// captured at registration would go stale the moment the function was renamed.
// This is the same single read the secrets used to cost.
func runFunction(ctx context.Context, app core.App, functionsDir, functionId, payload string, maxQueue int, recordId string) {
	// Check queue depth before blocking on the semaphore
	if maxQueue > 0 {
		val, _ := cronQueueDepth.LoadOrStore(functionId, &atomic.Int32{})
		counter := val.(*atomic.Int32)
		depth := counter.Add(1)
		defer counter.Add(-1)

		if int(depth) > maxQueue {
			app.Logger().Warn("faasbox cron: queue full, skipping",
				"functionId", functionId, "depth", depth, "maxQueue", maxQueue)
			return
		}
	}

	// Acquire semaphore (block until a slot is available)
	sem <- struct{}{}
	defer func() { <-sem }()

	if payload == "" {
		payload = "{}"
	}

	fn, err := app.FindRecordById(faasboxFunctionsCollection, functionId)
	if err != nil {
		app.Logger().Error("faasbox cron: function no longer exists, skipping",
			"functionId", functionId, "error", err)
		return
	}
	name := fn.GetString("name")

	env := functionEnv(app, fn)
	res := executeFunction(ctx, functionsDir, fn.Id, name, payload, env)

	// Publish what the dependency safety net did, if anything. invokeHandler does
	// the same right after its own call — the two must not diverge.
	publishDepsOutcome(app, functionsDir, fn.Id, name, res)

	// Stamp the trigger time whatever the outcome: what is recorded is that the
	// job fired, not that it succeeded. Missed-run detection reads it back.
	markCronJobRun(app, recordId, time.Now())

	// Log to faasbox_logs collection
	status := "success"
	if res.TimedOut {
		status = "timeout"
	} else if res.Err != nil {
		status = "error"
	}
	recordExecution(app, logEntry{
		FunctionId:     fn.Id,
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

// markCronJobRun stamps lastRunAt on a cron job record. The write goes through a
// direct SQL update on purpose: app.Save would fire OnRecordAfterUpdateSuccess,
// which resyncs the whole scheduler — a minutely job would rebuild the job list
// every minute for nothing.
func markCronJobRun(app core.App, recordId string, at time.Time) {
	if recordId == "" {
		return
	}

	_, err := app.DB().NewQuery(
		"UPDATE " + faasboxCronJobsCollection + " SET lastRunAt = {:at} WHERE id = {:id}",
	).Bind(dbx.Params{
		"at": at.UTC().Format(types.DefaultDateLayout),
		"id": recordId,
	}).Execute()
	if err != nil {
		app.Logger().Error("faasbox cron: failed to stamp lastRunAt",
			"recordId", recordId, "error", err)
	}
}
