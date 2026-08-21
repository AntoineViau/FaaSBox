package main

import (
	"context"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// This file carries the "the server just came up" deadline: cron.go owns the
// scheduler and the expression, here we own the triggers that fire once, a
// given delay after boot. Same cut as cronmissed.go — one file, one deadline.

// maxStartupDelayMinutes bounds startupDelayMinutes. 1439 minutes is 23:59, all
// that the hours:minutes entry of the editor can express: the bound is the shape
// of the input rather than an arbitration. Past a day it is no longer a startup
// trigger but a schedule, and cron carries that better.
const maxStartupDelayMinutes = 1439

// startupDelayUnit is the unit startupDelayMinutes counts in. A var and not a
// const, on the documented model of depsTimeout: a test proving that a delay of
// n units is actually waited cannot wait n minutes to prove it. It is the only
// seam here; everything else is exercised on a zero delay.
var startupDelayUnit = time.Minute

// scheduleStartupRuns arms every active startup trigger. It is the boot-time
// counterpart of syncAllCronJobs: the PocketBase scheduler cannot express "once,
// n minutes after the process came up", so each trigger gets its own timer.
//
// Registration is synchronous — a handful of records — but every wait and every
// run is detached: a zero delay would otherwise hold OnServe open for the whole
// execution timeout, and nothing would be listening yet.
//
// The race with installMissingDeps, launched detached just before, is not a
// problem and must not be handled: a zero-delay trigger that starts ahead of the
// dependency pass lands on ensureDeps inside executeFunction, which takes the
// directory lock and installs or waits. That is the existing safety net.
func scheduleStartupRuns(ctx context.Context, app core.App, functionsDir string) {
	records, err := app.FindAllRecords(faasboxCronJobsCollection)
	if err != nil {
		app.Logger().Error("faasbox startup: failed to load triggers", "error", err)
		return
	}

	for _, record := range records {
		if !record.GetBool("active") || cronKind(record) != "startup" {
			continue
		}

		functionId := record.GetString("function")
		if functionId == "" {
			continue
		}

		// Resolved here, like the scheduler does at registration: a dangling
		// relation is refused now rather than after the delay has elapsed, and
		// the function can be named in the line below. runFunction resolves
		// again when it fires, so a rename in between is picked up.
		fn, err := app.FindRecordById(faasboxFunctionsCollection, functionId)
		if err != nil {
			app.Logger().Error("faasbox startup: trigger points at no function, skipping",
				"recordId", record.Id, "functionId", functionId, "error", err)
			continue
		}

		payload := cronPayloadText(app, record)
		maxQueue := int(record.GetFloat("maxQueue"))
		delay := time.Duration(int(record.GetFloat("startupDelayMinutes"))) * startupDelayUnit
		recordId := record.Id

		app.Logger().Info("faasbox startup: trigger armed",
			"function", functionName(app, fn), "delay", delay)

		go func() {
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return
				}
			}
			runFunction(ctx, app, functionsDir, functionId, payload, maxQueue, recordId, "startup")
		}()
	}
}
