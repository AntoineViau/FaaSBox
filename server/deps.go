package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// depsMu holds a per-directory mutex to prevent concurrent bun install runs.
var depsMu sync.Map // map[string]*sync.Mutex

// depsHashFile records the dependency spec node_modules was built for. It lives
// inside node_modules so that removing the directory also invalidates the marker.
const depsHashFile = ".faasbox-deps"

// Installation states published on the faasbox_functions record. An empty value
// means the function declares no dependencies at all.
const (
	depsStatusPending    = "pending"    // install requested, not started yet
	depsStatusInstalling = "installing" // bun install is running
	depsStatusReady      = "ready"      // node_modules matches the current spec
	depsStatusError      = "error"      // last install failed, see depsError
)

// maxDepsError bounds the bun install output kept in depsError. A TextField with
// no explicit Max rejects the whole record past 5000 runes, and an install failure
// can print far more than that — the cap and the field size must move together.
const maxDepsError = 4 << 10

// depsOutputSlack is the room left inside maxDepsError for everything the
// captured install output gets wrapped in: the longest message prefix built
// below (91 bytes for the out-of-memory wording), the head marker tailWriter
// adds when it drops bytes (some forty), and the "failed to install
// dependencies: " an errDepsFailed puts in front on the invocation path
// (32 bytes).
//
// It exists because truncateForLog stays applied to the finished message and
// cuts the *tail* — the very end this capture went looking for. Sizing the
// capture at maxDepsError would push the total past the cap and hand that last
// rampart exactly the bytes that carry the diagnosis. With this margin it never
// cuts, and remains what it is meant to be: a rampart, not a participant.
const depsOutputSlack = 256

func newDepsStatusField() *core.SelectField {
	return &core.SelectField{
		Name:   "depsStatus",
		Values: []string{depsStatusPending, depsStatusInstalling, depsStatusReady, depsStatusError},
	}
}

func newDepsErrorField() *core.TextField {
	return &core.TextField{Name: "depsError", Max: maxDepsError + logMarkerSlack}
}

// depsHash returns a hash of package.json and bun.lock (when present).
// bun.lock is part of the hash because with --frozen-lockfile it is the lockfile,
// not package.json, that determines the installed tree.
func depsHash(funcDir string) (string, error) {
	pkg, err := os.ReadFile(filepath.Join(funcDir, "package.json"))
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(pkg)
	if lock, err := os.ReadFile(filepath.Join(funcDir, "bun.lock")); err == nil {
		h.Write(lock)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ensureDeps runs "bun install" in funcDir when the dependency spec changed since
// the last successful install. A per-directory lock prevents concurrent installs
// for the same function.
//
// installed reports that bun install actually ran and succeeded. Every early
// return — no package.json, unchanged spec — reports false, which is what lets
// the invocation path tell a real install from the hash check it pays on every
// call, and write the state only for the former.
func ensureDeps(ctx context.Context, funcDir string) (installed bool, err error) {
	// Normalise first: the per-directory lock below is keyed on this string, so a
	// caller passing a relative path would take a different lock than the
	// invocation path for the very same directory — and two bun installs would
	// then run concurrently on it.
	funcDir, err = filepath.Abs(funcDir)
	if err != nil {
		return false, fmt.Errorf("cannot resolve function directory: %w", err)
	}

	pkgPath := filepath.Join(funcDir, "package.json")
	if _, err := os.Stat(pkgPath); err != nil {
		return false, nil // no package.json → nothing to do
	}

	// Acquire a per-directory lock to prevent concurrent bun installs
	val, _ := depsMu.LoadOrStore(funcDir, &sync.Mutex{})
	mu := val.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	want, err := depsHash(funcDir)
	if err != nil {
		return false, nil // package.json vanished between the check and the lock
	}

	// Skip install if node_modules was built for this exact dependency spec
	modulesPath := filepath.Join(funcDir, "node_modules")
	if got, err := os.ReadFile(filepath.Join(modulesPath, depsHashFile)); err == nil && string(got) == want {
		return false, nil
	}

	// --ignore-scripts: npm lifecycle scripts would run outside every guard the
	// invocation path provides (no execution timeout, no bounded output capture),
	// so a single malicious transitive dependency would get a free hand.
	args := []string{"install", "--ignore-scripts"}
	lockPath := filepath.Join(funcDir, "bun.lock")
	if _, err := os.Stat(lockPath); err == nil {
		args = append(args, "--frozen-lockfile")
	}

	// Both streams land in the same bounded capture. bun appears to write
	// everything to stderr, but wiring stdout too costs nothing and settles the
	// question. The writer keeps the tail: bun prints its progress first and its
	// failure last, so the head is the half worth dropping.
	output := newTailWriter(maxDepsError - depsOutputSlack)
	cmd := exec.CommandContext(ctx, "bun", args...)
	cmd.Dir = funcDir
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.Env = []string{
		"HOME=/tmp",
		"PATH=/usr/local/bin:/usr/bin:/bin",
	}
	if err := cmd.Run(); err != nil {
		// exec.CommandContext kills through Process.Kill, so a deadline that
		// expires and a kill coming from outside both surface as "signal: killed".
		// Naming the cause is the whole point: an operator reading the field has
		// to know whether to raise the timeout or the memory.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false, fmt.Errorf("dependency install timed out: %s", output)
		}

		// SIGKILL while the deadline still had room: something outside killed the
		// process. The kernel leaves nothing behind in its victim, so the cause
		// cannot be named — only ruled out as neither bun nor a timeout.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signal() == syscall.SIGKILL {
				return false, fmt.Errorf(
					"dependency install was killed by the system before it finished, most likely out of memory: %s",
					output,
				)
			}
		}

		// An ordinary bun failure — missing package, desynchronised lockfile. Its
		// own output carries the information and is already readable.
		return false, fmt.Errorf("%w: %s", err, output)
	}

	// Best-effort marker: if writing it fails we simply reinstall next time.
	if err := os.MkdirAll(modulesPath, 0o755); err == nil {
		os.WriteFile(filepath.Join(modulesPath, depsHashFile), []byte(want), 0o644)
	}
	return true, nil
}

// scheduleDepsInstall installs the dependencies of a saved function in the
// background. The save moment is when the dependency spec is known to have
// changed, so the install belongs here rather than on the invocation path, where
// the caller would wait up to depsTimeout for it.
//
// ctx is the server lifecycle context: a shutdown cancels an install in flight
// instead of leaving it orphaned. The install runs detached from the save, which
// returns immediately.
func scheduleDepsInstall(ctx context.Context, app core.App, record *core.Record, functionsDir string) {
	name := record.GetString("name")
	if name == "" || !validName.MatchString(name) || len(name) > 64 {
		return
	}

	if record.GetString("packageJson") == "" {
		// No dependency spec: the state goes back to empty. Clearing unconditionally
		// covers the package.json that was just removed, where a leftover "ready"
		// would outlive its subject.
		setDepsState(app, record.Id, name, "", "")
		return
	}

	setDepsState(app, record.Id, name, depsStatusPending, "")

	recordId := record.Id
	funcDir := filepath.Join(functionsDir, name)

	go func() {
		setDepsState(app, recordId, name, depsStatusInstalling, "")

		installCtx, cancel := context.WithTimeout(ctx, depsTimeout)
		defer cancel()

		// ensureDeps returns on the hash check alone when the spec is unchanged,
		// so a save that only touched the script costs nothing here. Whether it
		// installed is of no use here: this path publishes the state either way,
		// having claimed it from pending onwards.
		if _, err := ensureDeps(installCtx, funcDir); err != nil {
			// A shutdown interrupted the install, it did not fail: the work is still
			// owed, and the safety net on the invocation path will do it. Reporting
			// an error here would outlive the restart and accuse the dependencies.
			if ctx.Err() != nil {
				setDepsState(app, recordId, name, depsStatusPending, "")
				return
			}
			app.Logger().Error("faasbox: dependency install failed", "function", name, "error", err)
			msg, _ := truncateForLog(err.Error(), maxDepsError)
			setDepsState(app, recordId, name, depsStatusError, msg)
			return
		}

		setDepsState(app, recordId, name, depsStatusReady, "")
	}()
}

// publishDepsOutcome records on the function record what the safety net on the
// invocation path just did.
//
// It belongs to the callers of executeFunction and not to the engine: exec.go
// holds no core.App and must not grow one — it does not know the database.
// invokeHandler and runFunction both hold one already, so the parity between the
// two paths is kept by both calling this.
//
// An invocation that left ensureDeps on the hash check — the overwhelming
// majority — writes nothing: the state is already right, and a write per
// invocation would be pure cost.
func publishDepsOutcome(app core.App, functionName string, res execResult) {
	var depsFailed *errDepsFailed
	if errors.As(res.Err, &depsFailed) {
		// The cause, not the errDepsFailed wrapper: the field must read the same
		// whether the install was triggered by a save or by this safety net.
		msg, _ := truncateForLog(depsFailed.Cause.Error(), maxDepsError)
		setDepsStateByName(app, functionName, depsStatusError, msg)
		return
	}
	if res.DepsInstalled {
		setDepsStateByName(app, functionName, depsStatusReady, "")
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
