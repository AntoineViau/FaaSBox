package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// What a trigger is, whatever its kind: the collection and its schema, the
// accessors of its encrypted columns, and the execution it fires when nobody is
// waiting on the other end.
//
// cron.go holds what only a five-field expression is worth — the validation of
// that expression and the PocketBase scheduler built from it. Keeping the two
// together would have named this collection after one of the three kinds it
// carries.

// triggerQueueDepth tracks the number of in-flight (waiting + running) trigger
// executions per function, keyed on the function id — the depth of a function
// must not reset because someone renamed it mid-flight.
var triggerQueueDepth sync.Map // map[string]*atomic.Int32

const faasboxTriggersCollection = "faasbox_triggers"

// ensureTriggersCollection creates the faasbox_triggers collection if it doesn't
// exist.
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
func ensureTriggersCollection(app core.App) error {
	if _, err := app.FindCollectionByNameOrId(faasboxTriggersCollection); err == nil {
		return nil
	}

	functions, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		return fmt.Errorf("collection %s not found: %w", faasboxFunctionsCollection, err)
	}

	col := core.NewBaseCollection(faasboxTriggersCollection)
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

// triggerName, triggerSchedule and triggerPayloadText are the only way to read
// the three encrypted columns of a trigger. Nothing else touches them, whichever
// accessor.
func triggerName(app core.App, record *core.Record) string {
	return decryptedText(app, record, "name")
}

func triggerSchedule(app core.App, record *core.Record) string {
	return decryptedText(app, record, "schedule")
}

// triggerPayloadText returns the payload as JSON text.
func triggerPayloadText(app core.App, record *core.Record) string {
	return decryptedJSON(app, record, "payload")
}

// triggerKind reads the trigger kind. An empty value reads as "cron": that is the
// shape every record had before startup triggers existed, and the one the
// PocketBase admin writes when it leaves the select untouched.
//
// This is the only place that normalisation happens. Neither the hook nor the
// management contract replays the default — two places normalising the same
// absence diverge at the third caller.
func triggerKind(record *core.Record) string {
	if kind := record.GetString("kind"); kind != "" {
		return kind
	}
	return "cron"
}

// runFunction executes a function outside of an HTTP context, for a trigger that
// nobody is waiting on. maxQueue limits how many executions (waiting + running)
// can exist simultaneously for this function. 0 means no limit. recordId
// identifies the faasbox_triggers record whose lastRunAt is stamped once the
// execution is over; an empty value skips the stamping. trigger is what the log
// entry carries — "cron" or "startup"; each caller is a single one and knows
// which, so it passes a constant.
//
// The function is resolved here, at fire time, and not captured when the trigger
// was registered: the scheduler is only rebuilt when a trigger record changes, so
// a name captured at registration would go stale the moment the function was
// renamed. This is the same single read the secrets used to cost.
func runFunction(ctx context.Context, app core.App, functionsDir, functionId, payload string, maxQueue int, recordId, trigger string) {
	// Check queue depth before blocking on the semaphore
	if maxQueue > 0 {
		val, _ := triggerQueueDepth.LoadOrStore(functionId, &atomic.Int32{})
		counter := val.(*atomic.Int32)
		depth := counter.Add(1)
		defer counter.Add(-1)

		if int(depth) > maxQueue {
			app.Logger().Warn("faasbox trigger: queue full, skipping",
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
		app.Logger().Error("faasbox trigger: function no longer exists, skipping",
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
	// trigger fired, not that it succeeded. Missed-run detection reads it back.
	markTriggerRun(app, recordId, time.Now())

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
		app.Logger().Error("faasbox trigger: execution failed",
			"function", name, "error", res.Err, "stdout", res.Stdout, "stderr", res.Stderr, "truncated", res.Truncated)
		return
	}

	app.Logger().Info("faasbox trigger: executed",
		"function", name, "stdout", res.Stdout, "stderr", res.Stderr, "truncated", res.Truncated)
}

// markTriggerRun stamps lastRunAt on a trigger record. The write goes through a
// direct SQL update on purpose: app.Save would fire OnRecordAfterUpdateSuccess,
// which resyncs the whole scheduler — a minutely job would rebuild the job list
// every minute for nothing.
func markTriggerRun(app core.App, recordId string, at time.Time) {
	if recordId == "" {
		return
	}

	_, err := app.DB().NewQuery(
		"UPDATE " + faasboxTriggersCollection + " SET lastRunAt = {:at} WHERE id = {:id}",
	).Bind(dbx.Params{
		"at": at.UTC().Format(types.DefaultDateLayout),
		"id": recordId,
	}).Execute()
	if err != nil {
		app.Logger().Error("faasbox trigger: failed to stamp lastRunAt",
			"recordId", recordId, "error", err)
	}
}
