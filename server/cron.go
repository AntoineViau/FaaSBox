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

// This file carries two subjects at once, and that is why it runs past the
// 300-line guideline: what any trigger is — the collection, its encrypted
// column accessors, runFunction and the queue depth — and what only a cron
// trigger is, the five-field expression and the PocketBase scheduler built
// from it. The name says the second; the top half serves the startup trigger
// of cronstartup.go just as much.

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
// The collection holds every trigger, not only the scheduled ones: kind tells a
// cron trigger from a startup one, and startupDelayMinutes says how long after
// boot the latter fires.
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
			// Not Required: a startup trigger carries no expression. The refusal
			// of a blank schedule on a cron trigger moves to validateTriggerHook,
			// which is the only place that knows which kind it is weighing.
			&core.TextField{Name: "schedule"},
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
			// Left in the clear, like active, maxQueue and lastRunAt: a
			// discriminant and a delay say nothing about the business content.
			// Not Required either — see triggerKind for what an empty value reads as.
			&core.SelectField{Name: "kind", MaxSelect: 1, Values: []string{"cron", "startup"}},
			&core.NumberField{Name: "startupDelayMinutes"},
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

// cronName, cronSchedule and cronPayloadText are the only way to read the three
// encrypted columns of a trigger. Nothing else touches them, whichever accessor.
func cronName(app core.App, record *core.Record) string {
	return decryptedText(app, record, "name")
}

func cronSchedule(app core.App, record *core.Record) string {
	return decryptedText(app, record, "schedule")
}

// cronPayloadText returns the payload as JSON text.
func cronPayloadText(app core.App, record *core.Record) string {
	return decryptedJSON(app, record, "payload")
}

// cronKind reads the trigger kind. An empty value reads as "cron": that is the
// shape every record had before startup triggers existed, and the one the
// PocketBase admin writes when it leaves the select untouched.
//
// This is the only place that normalisation happens. Neither the hook nor the
// management contract replays the default — two places normalising the same
// absence diverge at the third caller.
func cronKind(record *core.Record) string {
	if kind := record.GetString("kind"); kind != "" {
		return kind
	}
	return "cron"
}

// validateTriggerHook weighs a trigger record against the rules of its kind: a
// cron trigger needs an expression that parses, a startup trigger needs no
// expression at all and a delay within bounds.
//
// Every refusal is an ApiError and not an ordinary error on purpose: the record
// endpoints wrap a hook failure through firstApiError, which keeps the first
// argument that already is an ApiError and discards anything else. An ordinary
// error is replaced by a bare "Failed to create record", and the client is left
// with nothing to show. The messages are written for the user — the cron library
// error talks about internal field bounds and belongs in the server log, not in
// the response.
func validateTriggerHook(e *core.RecordEvent) error {
	// Read through the accessor, not off the column. A partial update — a
	// trigger merely toggled off — arrives carrying the schedule loaded from the
	// database, which is sealed: parsing that as a cron expression would refuse
	// every such save. The hook is bound before the encryption hook so the
	// submitted value it weighs is still the plaintext, and the accessor is what
	// makes the case the caller did not submit work too.
	schedule := cronSchedule(e.App, e.Record)

	if cronKind(e.Record) == "startup" {
		if schedule != "" {
			return apis.NewBadRequestError(
				"A startup trigger carries no schedule. Clear the schedule, or set the kind to \"cron\".",
				nil)
		}
		// GetFloat yields 0 for anything unparsable, so the fractional check is
		// what catches a delay sent as 3.5 — the column is a whole number of
		// minutes and nothing rounds it later.
		delay := e.Record.GetFloat("startupDelayMinutes")
		if delay < 0 || delay != float64(int(delay)) || int(delay) > maxStartupDelayMinutes {
			return apis.NewBadRequestError(fmt.Sprintf(
				"Invalid startup delay %v. Expected a whole number of minutes between 0 and %d.",
				delay, maxStartupDelayMinutes,
			), nil)
		}
		return e.Next()
	}

	if schedule == "" {
		return apis.NewBadRequestError(
			"A cron trigger needs a schedule: five fields, minute hour day-of-month month day-of-week.",
			nil)
	}
	if _, err := cron.NewSchedule(schedule); err != nil {
		e.App.Logger().Debug("faasbox cron: rejected schedule",
			"schedule", schedule, "error", err)
		return apis.NewBadRequestError(fmt.Sprintf(
			"Invalid cron expression %q. Expected 5 fields: minute hour day-of-month month day-of-week.",
			schedule,
		), nil)
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

		// Startup triggers are armed by scheduleStartupRuns, not registered here.
		// The blank-schedule guard below would drop them anyway, but the reader
		// must not have to deduce the intent from a side effect.
		if cronKind(record) == "startup" {
			continue
		}

		functionId := record.GetString("function")
		schedule := cronSchedule(app, record)
		payload := cronPayloadText(app, record)
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
			runFunction(ctx, app, functionsDir, functionId, payload, maxQueue, recordId, "cron")
		})
		if err != nil {
			app.Logger().Error("faasbox: failed to register cron",
				"jobId", jobId, "schedule", schedule, "function", functionName(app, fn), "error", err)
		}
	}
}

// runFunction executes a function outside of an HTTP context, for a trigger that
// nobody is waiting on. maxQueue limits how many executions (waiting + running)
// can exist simultaneously for this function. 0 means no limit. recordId
// identifies the faasbox_cron_jobs record whose lastRunAt is stamped once the
// execution is over; an empty value skips the stamping. trigger is what the log
// entry carries — "cron" or "startup"; each caller is a single one and knows
// which, so it passes a constant.
//
// The function is resolved here, at fire time, and not captured when the job was
// registered: the scheduler is only rebuilt when a cron record changes, so a name
// captured at registration would go stale the moment the function was renamed.
// This is the same single read the secrets used to cost.
func runFunction(ctx context.Context, app core.App, functionsDir, functionId, payload string, maxQueue int, recordId, trigger string) {
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
	// Decrypted, like the invocation path: the same name goes on to
	// executeFunction, which injects it as FUNCTION_NAME. The contract holds on
	// both triggers or it holds on neither.
	name := functionName(app, fn)

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
		Trigger:        trigger,
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
