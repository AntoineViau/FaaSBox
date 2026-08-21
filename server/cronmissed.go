package main

import (
	"fmt"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/cron"
	"github.com/pocketbase/pocketbase/tools/types"
)

// maxMissedLookback caps how far back missed cron runs are counted. The interval
// has to be walked minute by minute (see countMissedRuns), which is 43200 trivial
// iterations per job at this bound — negligible. Past a month the exact count has
// no operational interest anyway.
const maxMissedLookback = 30 * 24 * time.Hour

// countMissedRuns returns how many times the schedule was due over the half-open
// interval ]since, now[ — from the minute after since up to, but excluding, the
// current minute, which may still be about to fire.
//
// cron.Schedule only answers "is it due at this moment" (no Next(), no Prev()),
// so the interval is walked minute by minute. Moments are built in UTC, the
// timezone the PocketBase scheduler ticks in.
//
// The walk starts at most maxMissedLookback before now; the second return value
// reports whether that cap moved the start, in which case the count is a lower
// bound.
func countMissedRuns(schedule *cron.Schedule, since, now time.Time) (int, bool) {
	start := since.UTC().Truncate(time.Minute).Add(time.Minute)
	end := now.UTC().Truncate(time.Minute)

	capped := false
	if floor := end.Add(-maxMissedLookback); start.Before(floor) {
		start = floor
		capped = true
	}

	count := 0
	for t := start; t.Before(end); t = t.Add(time.Minute) {
		if schedule.IsDue(cron.NewMoment(t)) {
			count++
		}
	}
	return count, capped
}

// reportMissedCronRuns writes one warning and one faasbox_logs entry per active
// job that was due while the server was down. One entry per job and per startup,
// never one per occurrence: a minutely job over a two-day outage would otherwise
// insert 2880 rows and flush the whole log collection through the pruner.
func reportMissedCronRuns(app core.App, now time.Time) {
	records, err := app.FindAllRecords(faasboxTriggersCollection)
	if err != nil {
		app.Logger().Error("faasbox cron: failed to load triggers for missed run detection", "error", err)
		return
	}

	for _, record := range records {
		if !record.GetBool("active") {
			continue
		}

		expr := triggerSchedule(app, record)
		functionId := record.GetString("function")
		if expr == "" || functionId == "" {
			continue
		}

		// Same resolution as the scheduler: the relation says which function, the
		// record says what it is called right now.
		fn, err := app.FindRecordById(faasboxFunctionsCollection, functionId)
		if err != nil {
			app.Logger().Error("faasbox cron: cron trigger points at no function, skipping missed run detection",
				"recordId", record.Id, "functionId", functionId, "error", err)
			continue
		}
		name := functionName(app, fn)

		schedule, err := cron.NewSchedule(expr)
		if err != nil {
			// Already rejected at save time; a stored expression can only be
			// invalid if it was written outside the validation hook.
			app.Logger().Error("faasbox cron: invalid schedule, skipping missed run detection",
				"recordId", record.Id, "schedule", expr, "error", err)
			continue
		}

		// A job that never ran is measured from its creation date.
		since := record.GetDateTime("lastRunAt").Time()
		if since.IsZero() {
			since = record.GetDateTime("created").Time()
		}
		if since.IsZero() {
			continue
		}

		missed, capped := countMissedRuns(schedule, since, now)
		if missed == 0 {
			// Deliberately silent even when capped: a schedule whose only missed
			// occurrences predate the lookback bound cannot be observed without
			// walking past it. Reporting on the truncation flag alone would
			// assert a miss nobody saw — the walk simply never looked there.
			continue
		}

		// Two dates that say different things: countedFrom is how far back the
		// walk went, from is when the job last ran. Collapsing them would lose
		// the more useful one — since when the schedule has been silent.
		from := since.UTC().Format(types.DefaultDateLayout)
		countedFrom := from
		message := fmt.Sprintf(
			"%d scheduled runs were missed between %s and %s: the server was not running.",
			missed, from, now.UTC().Format(types.DefaultDateLayout))
		if capped {
			countedFrom = now.UTC().Add(-maxMissedLookback).Format(types.DefaultDateLayout)
			message = fmt.Sprintf(
				"%d scheduled runs were missed since %s (count capped at %d days); the job has not run since %s, so older occurrences were not counted.",
				missed, countedFrom, int(maxMissedLookback.Hours()/24), from)
		}

		app.Logger().Warn("faasbox cron: missed runs detected",
			"function", name, "schedule", expr, "missed", missed,
			"since", from, "countedFrom", countedFrom, "capped", capped)

		recordExecution(app, logEntry{
			FunctionId:   fn.Id,
			FunctionName: name,
			Trigger:      "cron",
			Status:       "missed",
			Stderr:       message,
		})
	}
}
