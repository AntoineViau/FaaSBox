package main

import (
	"context"
	"fmt"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/cron"
)

// What only a five-field expression is worth: the rule a trigger record is
// weighed against, and the PocketBase scheduler built from the expressions that
// pass it.
//
// Everything a trigger is regardless of its kind — the collection, the accessors
// of its encrypted columns, the execution itself — lives in triggers.go. A
// startup trigger crosses this file only to be skipped.

const cronJobPrefix = "__faasboxCron_"

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
	schedule := triggerSchedule(e.App, e.Record)

	if triggerKind(e.Record) == "startup" {
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
	// Remove all existing FaaS cron jobs
	for _, job := range app.Cron().Jobs() {
		id := job.Id()
		if len(id) > len(cronJobPrefix) && id[:len(cronJobPrefix)] == cronJobPrefix {
			app.Cron().Remove(id)
		}
	}

	// Load active trigger records
	records, err := app.FindAllRecords(faasboxTriggersCollection)
	if err != nil {
		app.Logger().Error("faasbox: failed to load triggers", "error", err)
		return
	}

	for _, record := range records {
		if !record.GetBool("active") {
			continue
		}

		// Startup triggers are armed by scheduleStartupRuns, not registered here.
		// The blank-schedule guard below would drop them anyway, but the reader
		// must not have to deduce the intent from a side effect.
		if triggerKind(record) == "startup" {
			continue
		}

		functionId := record.GetString("function")
		schedule := triggerSchedule(app, record)
		payload := triggerPayloadText(app, record)
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
			app.Logger().Error("faasbox: cron trigger points at no function, skipping",
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
