package main

import (
	"context"
	"fmt"
	"slices"
	"time"

	// The timezone database, embedded in the binary. The release image is an
	// alpine without the tzdata package and the binary is built CGO_ENABLED=0:
	// nothing else would answer time.LoadLocation("Europe/Paris") in production,
	// and every trigger would silently fall back to UTC.
	_ "time/tzdata"

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

// everyMinute is the expression every FaaS job is registered on, whatever the
// one its record carries, and that is deliberate: cron.Cron holds a single
// timezone for the whole scheduler (SetTimezone, applied in runDue), so a zone
// per record cannot travel through the registered expression. Each job wakes up
// every minute and decides for itself, in its own zone. The cost is one
// goroutine per active trigger per minute, negligible at this scale.
const everyMinute = "* * * * *"

// cronNow is the clock the registered closure reads to decide whether it is due.
// A variable rather than a direct time.Now call, on the documented model of
// startupDelayUnit and depsTimeout: a test that fires a job cannot wait until
// three in the morning to prove the gate opens, and pinning the wall clock is
// the only way to weigh a filter that reads it.
//
// The closure reads the clock itself because PocketBase calls go j.Run() without
// handing the job its tick instant. The gap between the tick and the read is of
// the order of a microsecond; only a delay past fifty-nine seconds crossing a
// minute boundary would move the decision.
var cronNow = time.Now

// hostClockNames are the names time.LoadLocation resolves that designate a
// *clock* rather than a place, and that must never reach the column.
//
// "Local" is a special value of the time package: LoadLocation accepts it and
// yields the timezone of the running process (TZ, else /etc/localtime).
// "localtime" and "posixrules" are entries a host tzdata package leaves in
// /usr/share/zoneinfo — the first a symlink to /etc/localtime, so the same thing
// under another spelling; the second a zone the distribution pinned, which the
// embedded database does not carry at all.
//
// One property is shared by the three, and it is the reason for the list: what
// they mean depends on the machine, not on a place. The same record would say
// Europe/Paris in development and something else — or nothing, falling back to
// UTC — in the release image, and a database replicated to another host would
// change meaning on the way. That is exactly the property this column exists to
// establish.
//
// They are reachable at all because time.LoadLocation reads the host's zoneinfo
// directory *before* the embedded database: loadLocation walks its sources, then
// falls back to loadFromEmbeddedTZData. The release image carries no such
// directory, so there the two readings already agree; this list is what makes
// them agree in development too. It closes the three known names, not the class
// — any entry a host carries and the embedded database does not has the same
// shape — but that residue exists in development only.
var hostClockNames = []string{"Local", "localtime", "posixrules"}

// triggerTimezone reads the zone an expression is evaluated in. An empty column
// reads as "UTC": that is the shape every record had before, and the one the
// PocketBase admin writes when the field is left untouched. Only point of
// normalisation, like triggerKind for the kind.
//
// It lives here rather than beside the other accessors in triggers.go because it
// carries what a five-field expression is worth and not what a trigger is: a
// startup trigger has no clock face to read.
func triggerTimezone(record *core.Record) string {
	if tz := record.GetString("timezone"); tz != "" {
		return tz
	}
	return "UTC"
}

// triggerLocation resolves that name. A zone that does not resolve falls back to
// UTC with a log line: validateTriggerHook refuses one at save time, so this
// branch is only reached on a record written outside the hook, or on a timezone
// database that vanished from under the binary.
func triggerLocation(app core.App, record *core.Record) *time.Location {
	name := triggerTimezone(record)
	loc, err := time.LoadLocation(name)
	if err != nil {
		app.Logger().Error("faasbox cron: unknown timezone, falling back to UTC",
			"recordId", record.Id, "timezone", name, "error", err)
		return time.UTC
	}
	return loc
}

// cronJobIsDue says whether an expression is due at this instant, seen from its
// own zone. The projection is the only place the timezone comes in: the instant
// stays absolute, so a daylight-saving change is taken without dedicated code.
//
// Extracted because two callers need it — the closure syncAllCronJobs registers
// and the missed-run walk — and a projection written twice would diverge.
func cronJobIsDue(s *cron.Schedule, loc *time.Location, at time.Time) bool {
	return s.IsDue(cron.NewMoment(at.In(loc)))
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
	schedule := triggerSchedule(e.App, e.Record)

	// Weighed before the kind branch, so a zone that does not resolve is refused
	// whatever the kind — including on a startup trigger, where it is inert. No
	// record may carry an unknown zone.
	if name := e.Record.GetString("timezone"); name != "" {
		// Two ways to be refused, one message: a name that does not resolve, and
		// a name that resolves to a clock rather than a place (cf.
		// hostClockNames). What is expected is an IANA zone name, and neither is
		// one.
		_, err := time.LoadLocation(name)
		if err != nil || slices.Contains(hostClockNames, name) {
			return apis.NewBadRequestError(fmt.Sprintf(
				"Unknown timezone %q. Expected an IANA zone name such as \"Europe/Paris\", or nothing at all for UTC.",
				name), nil)
		}
	}

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

		// Parsed here rather than handed to the scheduler: the job below is
		// registered on everyMinute, so nothing downstream would weigh the
		// expression any more.
		parsed, err := cron.NewSchedule(schedule)
		if err != nil {
			// Already refused at save time; a stored expression can only be
			// invalid if it was written outside the validation hook.
			app.Logger().Error("faasbox cron: invalid schedule, skipping registration",
				"recordId", record.Id, "schedule", schedule, "error", err)
			continue
		}
		loc := triggerLocation(app, record)

		jobId := cronJobPrefix + record.Id
		recordId := record.Id
		// The envelope is built at registration, like the schedule and the
		// payload beside it: renaming a trigger rewrites its record, and a
		// rewritten record rebuilds the whole job list. Nothing here can go
		// stale that the resync does not already refresh.
		in := newTriggerInput(triggerCron, triggerName(app, record), payload)
		err = app.Cron().Add(jobId, everyMinute, func() {
			if !cronJobIsDue(parsed, loc, cronNow()) {
				return
			}
			runFunction(ctx, app, functionsDir, functionId, in, maxQueue, recordId)
		})
		if err != nil {
			app.Logger().Error("faasbox: failed to register cron",
				"jobId", jobId, "schedule", schedule, "function", functionName(app, fn), "error", err)
		}
	}
}
