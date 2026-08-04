package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// This file carries what the function record holds of a dependency install —
// the published state and the resolved lockfile — and how it gets written.
// deps.go runs the install; here we only record its outcome. Both live in
// package main, so the split is about responsibility, not visibility.

// publishDepsOutcome records on the function record what the safety net on the
// invocation path just did.
//
// It belongs to the callers of executeFunction and not to the engine: exec.go
// holds no core.App and must not grow one — it does not know the database.
// invokeHandler and runFunction both hold one already, so the parity between the
// two paths is kept by both calling this.
//
// functionsDir is taken for the lockfile alone: persisting it needs the directory
// bun just wrote in, and the engine does not report the file, only that it ran.
//
// An invocation that left ensureDeps on the hash check — the overwhelming
// majority — writes nothing: the state is already right, and a write per
// invocation would be pure cost.
func publishDepsOutcome(app core.App, functionsDir, functionName string, res execResult) {
	var depsFailed *errDepsFailed
	if errors.As(res.Err, &depsFailed) {
		// The cause, not the errDepsFailed wrapper: the field must read the same
		// whether the install was triggered by a save or by this safety net.
		msg, _ := truncateForLog(depsFailed.Cause.Error(), maxDepsError)
		setDepsStateByName(app, functionName, depsStatusError, msg)
		return
	}
	if res.DepsInstalled {
		// Before the state, so that a record announcing "ready" already carries
		// what the install resolved.
		persistLockfile(app, functionsDir, functionName)
		setDepsStateByName(app, functionName, depsStatusReady, "")
	}
}

// persistLockfile stores what a successful install resolved, so the pinning
// survives a restart on a fresh filesystem. Without it the lockfile is state that
// only ever lives on disk: the same record installs differently depending on
// whether the container restarted, and every version range is re-resolved on a
// rebuilt filesystem.
//
// It is called by the three paths that can complete an install — the save, and
// the safety net on each of the two invocation paths.
func persistLockfile(app core.App, functionsDir, name string) {
	data, err := os.ReadFile(filepath.Join(functionsDir, name, "bun.lock"))
	if err != nil {
		return // no lockfile: nothing to persist, and nothing abnormal
	}

	// Never truncate. Unlike depsError, a cut lockfile is not degraded
	// information, it is invalid input. Past the cap we write nothing and leave
	// the previous value alone: a stale lockfile still pins correctly everything
	// that did not change, and bun reconciles the rest.
	if len(data) > maxLockfileSize {
		app.Logger().Warn("faasbox: lockfile too large to persist",
			"function", name, "size", len(data), "max", maxLockfileSize)
		return
	}

	setBunLock(app, name, string(data))
}

// setBunLock writes the resolved lockfile on the function record, keyed on the
// name — which is what all three install paths hold, and which carries a unique
// index.
//
// Kept distinct from updateDepsState rather than mutualised with it. The two share
// no logic: they set different columns, for different reasons, and only one of
// them broadcasts afterwards. What looks common is the three-line dbx idiom, and
// folding it into a generic column writer would mean building the SET clause from
// a map — machinery that costs more in indirection than the lines it saves.
// markCronJobRun does its own targeted UPDATE for the same reason.
//
// The reasons app.Save is out of the question are the ones documented on
// updateDepsState: it would fire OnRecordAfterUpdateSuccess, hence a new install,
// and it would rewrite the whole record from a copy loaded up to a minute earlier,
// overwriting a user save made in the meantime.
func setBunLock(app core.App, name, lock string) {
	_, err := app.DB().NewQuery(
		"UPDATE " + faasboxFunctionsCollection + " SET bunLock = {:lock} WHERE name = {:name}",
	).Bind(dbx.Params{
		"lock": lock,
		"name": name,
	}).Execute()
	if err != nil {
		app.Logger().Error("faasbox: failed to record the lockfile",
			"function", name, "error", err)
	}
}

// setDepsState publishes the installation state on the function record, then
// pushes it to the realtime subscribers. name is carried for the broadcast
// alone — the write itself is keyed on the record id.
func setDepsState(app core.App, recordId, name, status, errMsg string) {
	if !updateDepsState(app, "id", recordId, status, errMsg) {
		return
	}
	broadcastDepsState(app, name, status, errMsg)
}

// setDepsStateByName is the same write keyed on the function name, which is what
// the invocation path holds — it never loaded the record. The name column carries
// a unique index, so it identifies exactly one row.
func setDepsStateByName(app core.App, name, status, errMsg string) {
	if !updateDepsState(app, "name", name, status, errMsg) {
		return
	}
	broadcastDepsState(app, name, status, errMsg)
}

// updateDepsState writes the two dependency columns through a direct SQL update
// and reports whether it went through.
//
// The write stays targeted for two reasons, and the second is the one that
// forbids app.Save outright. It would fire OnRecordAfterUpdateSuccess, hence a
// new install, hence a new state write — a guard on record.Original() could
// close that loop. But app.Save also rewrites the *whole* record, and the
// install goroutine holds one loaded up to a minute earlier: a user save made in
// the meantime would be silently overwritten. Re-reading just before writing
// narrows the window without closing it, and PocketBase offers no optimistic
// locking. Writing only the two columns this code owns is the correct move, and
// broadcasting is not a reason to give it up.
//
// column is one of two literals from this file, never caller input.
func updateDepsState(app core.App, column, value, status, errMsg string) bool {
	_, err := app.DB().NewQuery(
		"UPDATE " + faasboxFunctionsCollection +
			" SET depsStatus = {:status}, depsError = {:err} WHERE " + column + " = {:value}",
	).Bind(dbx.Params{
		"status": status,
		"err":    errMsg,
		"value":  value,
	}).Execute()
	if err != nil {
		app.Logger().Error("faasbox: failed to record dependency state",
			column, value, "status", status, "error", err)
		return false
	}
	return true
}
