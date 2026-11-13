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

// invocationPath is what one caller of the verb decides: which function a
// segment designates for it, and what stdout must be to count as an answer.
//
// Three callers share the verb and differ on exactly these two points. /invoke
// and MCP resolve under the scope of a key and accept any stdout; /f/ resolves
// only what a public route opened and requires a response envelope. Everything
// else — the slot, the dependency outcome, the status of the run, its log entry
// — is the verb's, and is written once, below.
type invocationPath struct {
	resolve func(app core.App, idOrName string) (*core.Record, error)
	decode  func(stdout string, truncated bool) (any, error)
}

// scopedInvocation is the path /invoke and invoke_function take: the function
// is resolved under the scope of the key that asked, and stdout is decoded as
// it always was.
func scopedInvocation(allowed []string) invocationPath {
	return invocationPath{
		resolve: func(app core.App, idOrName string) (*core.Record, error) {
			return scopedFunction(app, allowed, idOrName)
		},
		decode: parseFunctionOutput,
	}
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
//
// path says how the segment resolves and what stdout must be (invocationPath).
// in carries the envelope stdin receives and the trigger the log entry records.
// Both are built by the caller — invoke.go and pages.go from the request they
// hold, mcptools.go from an agent's arguments — because only the caller knows
// what shape the call had. This file stays what it was: the verb, with no
// request in sight.
func invokeFunction(ctx context.Context, app core.App, functionsDir string, path invocationPath, idOrName string, in executionInput) (invokeOutcome, error) {
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

	// Resolve the function the way this caller's path says, then execute it. The
	// record replaces the lookup lookupFunctionEnv used to make: the secrets come
	// off the row already read.
	fn, err := path.resolve(app, idOrName)
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
	res := executeFunction(ctx, functionsDir, fn.Id, name, in.Envelope, env)

	// Publish what the dependency safety net did, if anything. The cron path does
	// the same right after its own call — the two must not diverge.
	publishDepsOutcome(app, functionsDir, fn.Id, name, res)

	// Decode stdout before logging: an execution whose output is not what this
	// path takes for an answer — cut at the capture cap, or not an envelope on
	// /f/ — is a failure, and the log has to say so.
	var result any
	var decodeErr error
	if res.Err == nil {
		result, decodeErr = path.decode(res.Stdout, res.StdoutTruncated)
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
		} else if res.Err != nil || decodeErr != nil {
			status = "error"
		}
		recordExecution(app, logEntry{
			FunctionId:     fn.Id,
			FunctionName:   name,
			Trigger:        in.Trigger,
			Status:         status,
			DurationMs:     res.Duration.Milliseconds(),
			Stdout:         res.Stdout,
			Stderr:         res.Stderr,
			RequestPayload: in.Envelope,
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
	case decodeErr != nil:
		return outcome, decodeErr
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
//
// The refusal carries the cap in force when it is built, which is now — a test
// moves that variable under it.
func parseFunctionOutput(stdout string, truncated bool) (any, error) {
	var result any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		if truncated {
			return nil, &errOutputUnusable{limit: maxOutputSize}
		}
		return stdout, nil
	}
	return result, nil
}
