package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// This file carries the invocation as an operation, on the same cut as
// manageops.go: what invoking a function *is*, with no request in sight.
// invoke.go keeps the transport — read the body, bound it, turn what comes back
// into a status and some JSON.
//
// The engine itself was extracted long ago; what was not is everything around
// it — the concurrency slot, publishing what the dependency safety net did,
// decoding the output, recording the run. A second caller of the *verb* would
// have had to reproduce all of it, and a run it forgot to record is a run that
// left no trace anywhere.
//
// **The semaphore is acquired without blocking**, and that is a decision this
// file owns. runFunction (cron.go) blocks instead, because nobody is waiting on
// a scheduled run and the guard against pile-up is maxQueue. Here someone is
// waiting for an answer, whatever asked for it, and a caller left hanging is
// worse than a refusal it can retry. The asymmetry is the one the project
// already documents between the two paths; what changes is that it now belongs
// to the verb rather than to HTTP.

// errTooBusy marks an invocation refused because every execution slot was taken.
var errTooBusy = errors.New("too many concurrent invocations, try again later")

// errExecTimedOut marks a run the execution timeout cut short. It names the
// budget rather than relaying the context error, which says nothing to whoever
// called.
var errExecTimedOut = fmt.Errorf("function timed out after %s", execTimeout)

// errResolveFailed marks a lookup that failed for a reason of ours. The cause is
// logged where it happens; what the caller is told stays deliberately blank.
var errResolveFailed = errors.New("failed to resolve the function")

// errOutputUnusable marks a run that completed and whose result did not survive
// the capture cap. It carries the cap in force rather than reading it back at
// the response, because that cap is a variable a test moves under it.
type errOutputUnusable struct{ limit int }

func (e *errOutputUnusable) Error() string {
	return fmt.Sprintf(
		"function output exceeded the %d bytes capture limit and the truncated result is not valid JSON; raise FAASBOX_MAX_OUTPUT_SIZE or return less data",
		e.limit,
	)
}

// invokeOutcome is what one invocation publishes. It is filled even when the
// invocation failed: an error response says how long the failure took and what
// the function managed to write, and losing that on the way out would make a
// timeout indistinguishable from an instant refusal.
type invokeOutcome struct {
	Function  string
	Result    any
	Stdout    string
	Stderr    string
	Duration  time.Duration
	Truncated bool
}

// invokeFunction runs the function a caller designates and answers with what it
// produced. It is the other half of the pair runFunction forms: same engine,
// same publication of the dependency state, same log entry, and the two must not
// diverge (cf. the parity rule between the immediate and the scheduled path).
func invokeFunction(ctx context.Context, app core.App, functionsDir string, allowed []string, idOrName string, payload []byte) (invokeOutcome, error) {
	// Validated before anything is reserved: a segment that never had a chance
	// of designating a function must not cost a slot.
	if !validName.MatchString(idOrName) || len(idOrName) > 64 {
		return invokeOutcome{}, errInvalidFunctionName
	}

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	default:
		return invokeOutcome{}, errTooBusy
	}

	// Resolve the function, scope included, then execute it. The record replaces
	// the lookup lookupFunctionEnv used to make: the secrets come off the row
	// already read.
	fn, err := scopedFunction(app, allowed, idOrName)
	if err != nil {
		var notFound *errNotFound
		if errors.As(err, &notFound) || errors.Is(err, errScopeRestricted) {
			// Nothing ran, so nothing is recorded — same rule as an absent
			// index.ts.
			return invokeOutcome{}, err
		}
		app.Logger().Error("faasbox http: failed to resolve the function",
			"segment", idOrName, "error", err)
		return invokeOutcome{}, errResolveFailed
	}
	// Decrypted, and this is the reading that reaches furthest: it is handed to
	// executeFunction, which injects it as FUNCTION_NAME in the subprocess.
	name := functionName(app, fn)

	env := functionEnv(app, fn)
	res := executeFunction(ctx, functionsDir, fn.Id, name, string(payload), env)

	// Publish what the dependency safety net did, if anything. The cron path does
	// the same right after its own call — the two must not diverge.
	publishDepsOutcome(app, functionsDir, fn.Id, name, res)

	// Decode stdout before logging: an execution whose output did not survive the
	// capture cap is a failure, and the log has to say so.
	var result any
	outputUsable := true
	if res.Err == nil {
		result, outputUsable = parseFunctionOutput(res.Stdout, res.StdoutTruncated)
	}

	// Record the run. Only errNotFound stays out: nothing ran, so there is no
	// execution to record. A dependency install that failed did take place and
	// earns its line, at the same title as on the cron path — the path where
	// someone is waiting for an answer was the one explaining nothing.
	var notFound *errNotFound
	if !errors.As(res.Err, &notFound) {
		status := "success"
		if res.TimedOut {
			status = "timeout"
		} else if res.Err != nil || !outputUsable {
			status = "error"
		}
		recordExecution(app, logEntry{
			FunctionId:     fn.Id,
			FunctionName:   name,
			Trigger:        "http",
			Status:         status,
			DurationMs:     res.Duration.Milliseconds(),
			Stdout:         res.Stdout,
			Stderr:         res.Stderr,
			RequestPayload: string(payload),
			ExitCode:       res.ExitCode,
		})

		// Same rule for the server log, and the same reason: a failure the caller
		// is told about still has to leave something behind for whoever reads the
		// logs during an incident.
		if res.Err != nil {
			app.Logger().Error("faasbox http: execution failed",
				"function", name, "error", res.Err, "stdout", res.Stdout, "stderr", res.Stderr, "truncated", res.Truncated)
		}
	}

	outcome := invokeOutcome{
		Function:  name,
		Result:    result,
		Stdout:    res.Stdout,
		Stderr:    res.Stderr,
		Duration:  res.Duration,
		Truncated: res.Truncated,
	}

	switch {
	case res.TimedOut:
		return outcome, errExecTimedOut
	case res.Err != nil:
		return outcome, res.Err
	case !outputUsable:
		return outcome, &errOutputUnusable{limit: maxOutputSize}
	}
	return outcome, nil
}

// parseFunctionOutput decodes a function's stdout into the value returned as
// "result". Non-JSON output is handed back verbatim — writing free text is a
// legitimate way to answer. That fallback is refused when the capture was
// truncated: the function may well have produced valid JSON, and returning the
// surviving fragment as a plain string would pass a mutilated answer off as a
// good one. A truncation that happens to leave valid JSON behind is
// undetectable, and the "truncated" flag remains its only signal.
func parseFunctionOutput(stdout string, truncated bool) (result any, usable bool) {
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		if truncated {
			return nil, false
		}
		return stdout, true
	}
	return result, true
}
